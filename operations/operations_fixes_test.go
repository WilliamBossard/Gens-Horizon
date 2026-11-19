package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/WilliamBossard/Gens-Horizon/paths"
	"github.com/WilliamBossard/Gens-Horizon/providers"
	"github.com/WilliamBossard/Gens-Horizon/retry"
)

type stubDeleteProvider struct {
	deletedIDs []string
	errByID    map[string]error
}

func (s *stubDeleteProvider) Name() string { return "stub" }
func (s *stubDeleteProvider) ListFiles(string) ([]providers.CloudFile, error) {
	return nil, nil
}
func (s *stubDeleteProvider) DownloadFile(string, string, func(int), int64) error { return nil }
func (s *stubDeleteProvider) DownloadJSON(string, any) error                       { return nil }
func (s *stubDeleteProvider) UploadZip(string, string, string, func(int)) (string, error) {
	return "", nil
}
func (s *stubDeleteProvider) UploadJSON(string, any, string) (string, error) { return "", nil }
func (s *stubDeleteProvider) DeleteFile(fileID string) error {
	s.deletedIDs = append(s.deletedIDs, fileID)
	if err, ok := s.errByID[fileID]; ok {
		return err
	}
	return nil
}
func (s *stubDeleteProvider) GetQuota() (int64, int64, error) { return 0, 0, nil }

func TestCollectCleanItemsClockSkewSafe(t *testing.T) {
	baseTime := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	baseName := PrefixBackup + "Pack.zip"
	newerDeltaName := fmt.Sprintf("%sPack_%d.zip", PrefixDelta, baseTime.Add(-30*time.Second).UnixMilli())
	olderDeltaName := fmt.Sprintf("%sPack_%d.zip", PrefixDelta, baseTime.Add(5*time.Second).UnixMilli())

	cloudIndex := map[string]providers.CloudFile{
		baseName: {
			ID:           "base-1",
			Name:         baseName,
			ModifiedTime: baseTime,
			Size:         1000,
		},
		// Horloge client en retard : nom fichier antérieur à baseTime, mais ModifiedTime serveur POSTÉRIEUR à baseTime -> NE DOIT PAS être supprimé
		newerDeltaName: {
			ID:           "delta-newer-server",
			Name:         newerDeltaName,
			ModifiedTime: baseTime.Add(15 * time.Second),
			Size:         200,
		},
		// Delta dont le ModifiedTime serveur est ANTÉRIEUR à la base -> DOIT être marqué obsolète
		olderDeltaName: {
			ID:           "delta-older-server",
			Name:         olderDeltaName,
			ModifiedTime: baseTime.Add(-10 * time.Second),
			Size:         300,
		},
	}

	items, freed := collectCleanItems(cloudIndex)
	if len(items) != 1 || items[0].ID != "delta-older-server" || freed != 300 {
		t.Fatalf("unexpected clean items: got %+v (freed=%d), expected only delta-older-server", items, freed)
	}
}

func TestDetectUploadConflictWithoutLocalHistory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_conflict_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	syncPath := filepath.Join(tmpDir, "last_sync.json")
	base := providers.CloudFile{ID: "base-1", ModifiedTime: time.Now()}

	// Pas d'historique local + sauvegarde existante sur le Cloud sans --force -> conflit
	if msg := detectUploadConflict("Pack", true, false, base, providers.CloudFile{}, nil, syncPath, false); msg == "" {
		t.Fatalf("expected conflict when cloud backup exists and lastSyncTime == 0 without --force")
	}

	// Avec --force -> autorisé
	if msg := detectUploadConflict("Pack", true, false, base, providers.CloudFile{}, nil, syncPath, true); msg != "" {
		t.Fatalf("expected no conflict with --force, got %q", msg)
	}

	// Historique local récent + cloud inchangé -> autorisé
	syncJSON, _ := json.Marshal(map[string]string{
		"Pack": time.Now().Add(5 * time.Second).Format(time.RFC3339),
	})
	_ = os.WriteFile(syncPath, syncJSON, 0600)
	if msg := detectUploadConflict("Pack", true, false, base, providers.CloudFile{}, nil, syncPath, false); msg != "" {
		t.Fatalf("expected no conflict when local sync is newer than cloud, got %q", msg)
	}
}

func TestHandleDeletePropagatesErrorsAndAcceptsNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "horizon_delete_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	t.Setenv("HORIZON_DATA_DIR", tmpDir)
	_ = os.MkdirAll(filepath.Join(tmpDir, "manifests"), 0755)
	_ = os.MkdirAll(filepath.Join(tmpDir, "meta"), 0755)

	setupLocalFiles := func() {
		_ = os.WriteFile(paths.GetManifestPath("Pack"), []byte(`{}`), 0644)
		_ = os.WriteFile(paths.GetMetaPath("Pack"), []byte(`{}`), 0644)
		_ = os.WriteFile(paths.GetLastSyncPath(), []byte(`{"Pack":"2026-11-14T10:00:00Z","Other":"2026-11-14T10:00:00Z"}`), 0644)
	}

	cloudIndex := map[string]providers.CloudFile{
		PrefixBackup + "Pack.zip":    {ID: "id-base", Name: PrefixBackup + "Pack.zip"},
		PrefixManifest + "Pack.json": {ID: "id-manifest", Name: PrefixManifest + "Pack.json"},
	}

	// Cas 1 : erreur Cloud réelle -> l'état local doit rester intact
	setupLocalFiles()
	failingProv := &stubDeleteProvider{
		errByID: map[string]error{"id-manifest": errors.New("network timeout")},
	}
	handleDelete("Pack", cloudIndex, failingProv, tmpDir)
	if _, statErr := os.Stat(paths.GetManifestPath("Pack")); statErr != nil {
		t.Fatalf("expected local manifest to remain intact when cloud delete failed: %v", statErr)
	}
	syncBytes, _ := os.ReadFile(paths.GetLastSyncPath())
	var syncMap map[string]string
	_ = json.Unmarshal(syncBytes, &syncMap)
	if _, ok := syncMap["Pack"]; !ok {
		t.Fatalf("expected Pack entry in last_sync.json to be preserved on delete failure")
	}

	// Cas 2 : ErrNotFound sur un fichier déjà supprimé -> la suppression locale doit aboutir proprement
	notFoundProv := &stubDeleteProvider{
		errByID: map[string]error{"id-manifest": providers.ErrNotFound},
	}
	handleDelete("Pack", cloudIndex, notFoundProv, tmpDir)
	if _, statErr := os.Stat(paths.GetManifestPath("Pack")); !os.IsNotExist(statErr) {
		t.Fatalf("expected local manifest to be deleted when cloud returns ErrNotFound")
	}
	syncBytes2, _ := os.ReadFile(paths.GetLastSyncPath())
	syncMap = map[string]string{}
	_ = json.Unmarshal(syncBytes2, &syncMap)
	if _, ok := syncMap["Pack"]; ok {
		t.Fatalf("expected Pack entry in last_sync.json to be removed after successful delete")
	}
	if _, ok := syncMap["Other"]; !ok {
		t.Fatalf("expected unrelated Other entry in last_sync.json to remain intact")
	}
}

func TestSelectSyncDeltasSkipsDeltasOlderThanBaseOnFullExtract(t *testing.T) {
	baseTime := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	baseFile := providers.CloudFile{
		ID:           "base-1",
		Name:         PrefixBackup + "Pack.zip",
		ModifiedTime: baseTime,
	}
	oldDelta := providers.CloudFile{
		ID:           "delta-old",
		Name:         fmt.Sprintf("%sPack_%d.zip", PrefixDelta, baseTime.Add(-10*time.Minute).UnixMilli()),
		ModifiedTime: baseTime.Add(-10 * time.Minute),
	}
	newDelta := providers.CloudFile{
		ID:           "delta-new",
		Name:         fmt.Sprintf("%sPack_%d.zip", PrefixDelta, baseTime.Add(5*time.Minute).UnixMilli()),
		ModifiedTime: baseTime.Add(5 * time.Minute),
	}

	cloudIndex := map[string]providers.CloudFile{
		baseFile.Name: baseFile,
		oldDelta.Name: oldDelta,
		newDelta.Name: newDelta,
	}

	deltas := selectSyncDeltas(cloudIndex, "Pack", baseFile, 0, true)
	if len(deltas) != 1 || deltas[0].file.ID != "delta-new" {
		t.Fatalf("expected only delta-new after base consolidation, got %+v", deltas)
	}
}

func TestCopyDirSkipsIgnoredAndRestoreInPlace(t *testing.T) {
	src := t.TempDir()
	snapshot := t.TempDir()

	// Create normal files and ignored dirs/files
	_ = os.MkdirAll(filepath.Join(src, "config"), 0755)
	_ = os.MkdirAll(filepath.Join(src, "logs"), 0755)
	_ = os.WriteFile(filepath.Join(src, "config", "settings.toml"), []byte("v1"), 0644)
	_ = os.WriteFile(filepath.Join(src, "logs", "latest.log"), []byte("log-content"), 0644)
	_ = os.WriteFile(filepath.Join(src, "options.txt"), []byte("orig-options"), 0644)

	if err := copyDir(src, snapshot, true); err != nil {
		t.Fatalf("copyDir failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(snapshot, "logs", "latest.log")); !os.IsNotExist(err) {
		t.Fatalf("expected ignored logs/ to be skipped in rollback snapshot")
	}

	// Simulate partial sync mutations in src (detaching hardlink via Remove, as ExtractZip/ApplyDelta do)
	_ = os.Remove(filepath.Join(src, "options.txt"))
	_ = os.WriteFile(filepath.Join(src, "options.txt"), []byte("corrupted-options"), 0644)
	_ = os.WriteFile(filepath.Join(src, "stray.jar"), []byte("stray"), 0644)

	if err := restoreInPlace(snapshot, src); err != nil {
		t.Fatalf("restoreInPlace failed: %v", err)
	}

	gotOpts, err := os.ReadFile(filepath.Join(src, "options.txt"))
	if err != nil || string(gotOpts) != "orig-options" {
		t.Fatalf("expected options.txt restored to orig-options, got %q (err=%v)", string(gotOpts), err)
	}
	if _, err := os.Stat(filepath.Join(src, "stray.jar")); !os.IsNotExist(err) {
		t.Fatalf("expected stray file added after snapshot to be removed by restoreInPlace")
	}
	if _, err := os.Stat(filepath.Join(src, "logs", "latest.log")); err != nil {
		t.Fatalf("expected ignored logs/ file in target to remain untouched: %v", err)
	}
}

type mockStagedFile struct {
	id   string
	name string
}

type mockStagedProvider struct {
	files          map[string]mockStagedFile
	nextSeq        int
	failJSONName   string
	failRenameName string
}

func newMockStagedProvider() *mockStagedProvider {
	return &mockStagedProvider{
		files: map[string]mockStagedFile{
			"old-base-id":     {id: "old-base-id", name: "GensHorizon_Backup_Pack.zip"},
			"old-manifest-id": {id: "old-manifest-id", name: "GensHorizon_Manifest_Pack.json"},
		},
	}
}

func (m *mockStagedProvider) Name() string { return "mock-staged" }
func (m *mockStagedProvider) ListFiles(string) ([]providers.CloudFile, error) {
	return nil, nil
}
func (m *mockStagedProvider) DownloadFile(string, string, func(int), int64) error { return nil }
func (m *mockStagedProvider) DownloadJSON(string, any) error                       { return nil }
func (m *mockStagedProvider) UploadZip(name string, _ string, _ string, _ func(int)) (string, error) {
	m.nextSeq++
	id := fmt.Sprintf("zip-%d", m.nextSeq)
	m.files[id] = mockStagedFile{id: id, name: name}
	return id, nil
}
func (m *mockStagedProvider) UploadJSON(name string, _ any, _ string) (string, error) {
	if m.failJSONName != "" && (name == m.failJSONName || (len(name) > len("GensHorizon_Staging_") && m.failJSONName == "*")) {
		return "", errors.New("simulated manifest upload error")
	}
	m.nextSeq++
	id := fmt.Sprintf("json-%d", m.nextSeq)
	m.files[id] = mockStagedFile{id: id, name: name}
	return id, nil
}
func (m *mockStagedProvider) DeleteFile(fileID string) error {
	delete(m.files, fileID)
	return nil
}
func (m *mockStagedProvider) GetQuota() (int64, int64, error) { return 0, 0, nil }
func (m *mockStagedProvider) RenameFile(fileID string, newName string, existingTargetID string) (string, error) {
	if m.failRenameName != "" && newName == m.failRenameName {
		return "", errors.New("simulated rename error")
	}
	f, ok := m.files[fileID]
	if !ok {
		return "", providers.ErrNotFound
	}
	f.name = newName
	m.files[fileID] = f
	if existingTargetID != "" && existingTargetID != fileID {
		delete(m.files, existingTargetID)
	}
	return fileID, nil
}

func TestUploadFullBackupAtomicPreservesOldBackupOnManifestFailure(t *testing.T) {
	p := newMockStagedProvider()
	p.failJSONName = "*"
	tmpZip := filepath.Join(t.TempDir(), "pack.zip")
	_ = os.WriteFile(tmpZip, []byte("PK"), 0644)

	_, _, errCode, err := uploadFullBackupAtomic(
		p, "Pack", "Pack", tmpZip,
		"GensHorizon_Backup_Pack.zip", "old-base-id",
		"GensHorizon_Manifest_Pack.json", "old-manifest-id",
		nil,
		struct {
			MaxRetries int
			BaseDelay  time.Duration
			MaxDelay   time.Duration
			Label      string
		}{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Label: "test"},
	)
	if err == nil || errCode != "MANIFEST_UPLOAD_FAILED" {
		t.Fatalf("expected MANIFEST_UPLOAD_FAILED, got errCode=%q err=%v", errCode, err)
	}
	if _, ok := p.files["old-base-id"]; !ok {
		t.Fatalf("expected old-base-id to stay intact after staging failure")
	}
	if _, ok := p.files["old-manifest-id"]; !ok {
		t.Fatalf("expected old-manifest-id to stay intact after staging failure")
	}
	if len(p.files) != 2 {
		t.Fatalf("expected staged backup zip to be rolled back, remaining files: %+v", p.files)
	}
}

func TestUploadFullBackupAtomicPromotesStagingOnSuccess(t *testing.T) {
	p := newMockStagedProvider()
	tmpZip := filepath.Join(t.TempDir(), "pack.zip")
	_ = os.WriteFile(tmpZip, []byte("PK"), 0644)

	newBaseID, newMID, errCode, err := uploadFullBackupAtomic(
		p, "Pack", "Pack", tmpZip,
		"GensHorizon_Backup_Pack.zip", "old-base-id",
		"GensHorizon_Manifest_Pack.json", "old-manifest-id",
		nil,
		struct {
			MaxRetries int
			BaseDelay  time.Duration
			MaxDelay   time.Duration
			Label      string
		}{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Label: "test"},
	)
	if err != nil || errCode != "" {
		t.Fatalf("unexpected error: errCode=%q err=%v", errCode, err)
	}
	if p.files[newBaseID].name != "GensHorizon_Backup_Pack.zip" {
		t.Fatalf("expected promoted base name, got %q", p.files[newBaseID].name)
	}
	if p.files[newMID].name != "GensHorizon_Manifest_Pack.json" {
		t.Fatalf("expected promoted manifest name, got %q", p.files[newMID].name)
	}
	if _, ok := p.files["old-base-id"]; ok {
		t.Fatalf("expected old-base-id to be replaced/deleted")
	}
	if _, ok := p.files["old-manifest-id"]; ok {
		t.Fatalf("expected old-manifest-id to be replaced/deleted")
	}
}

func TestCollectCleanItemsFlagsStaleStaging(t *testing.T) {
	cloudIndex := map[string]providers.CloudFile{
		"GensHorizon_Staging_Pack_backup_123.zip": {
			ID:   "stg-1",
			Name: "GensHorizon_Staging_Pack_backup_123.zip",
			Size: 512,
		},
	}
	items, freed := collectCleanItems(cloudIndex)
	if len(items) != 1 || items[0].ID != "stg-1" || freed != 512 {
		t.Fatalf("expected stale staging file to be collected, got %+v (freed=%d)", items, freed)
	}
}

func TestUploadFullBackupAtomicCleansStagedBaseOnRenameFailure(t *testing.T) {
	p := newMockStagedProvider()
	p.failRenameName = "GensHorizon_Backup_Pack.zip"
	tmpZip := filepath.Join(t.TempDir(), "pack.zip")
	_ = os.WriteFile(tmpZip, []byte("PK"), 0644)

	_, _, errCode, err := uploadFullBackupAtomic(
		p, "Pack", "Pack", tmpZip,
		"GensHorizon_Backup_Pack.zip", "old-base-id",
		"GensHorizon_Manifest_Pack.json", "old-manifest-id",
		nil,
		struct {
			MaxRetries int
			BaseDelay  time.Duration
			MaxDelay   time.Duration
			Label      string
		}{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Label: "test"},
	)
	if err == nil || errCode != "UPLOAD_FAILED" {
		t.Fatalf("expected UPLOAD_FAILED, got errCode=%q err=%v", errCode, err)
	}
	if len(p.files) != 2 {
		t.Fatalf("expected both staged zip and staged manifest to be deleted on RenameFile failure, remaining: %+v", p.files)
	}
}

func TestSwapRestoreSnapshotPreservesIgnoredEntries(t *testing.T) {
	parent := t.TempDir()
	targetPath := filepath.Join(parent, "InstancePack")
	snapshotDir := filepath.Join(parent, "snapshot")

	// Prépare l'instance locale existante avec dossiers/fichiers ignorés et un fichier corrompu
	if err := os.MkdirAll(filepath.Join(targetPath, "logs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(targetPath, "saves", "world"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetPath, "logs", "latest.log"), []byte("keep-log"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetPath, "saves", "world", "session.lock"), []byte("keep-lock"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetPath, "options.txt"), []byte("corrupted"), 0644); err != nil {
		t.Fatal(err)
	}

	// Prépare le snapshot de rollback (ne contient pas logs/ ni session.lock)
	if err := os.MkdirAll(filepath.Join(snapshotDir, "saves", "world"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "options.txt"), []byte("restored"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "saves", "world", "level.dat"), []byte("lvl"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := swapRestoreSnapshot(snapshotDir, targetPath); err != nil {
		t.Fatalf("swapRestoreSnapshot failed: %v", err)
	}

	gotOpt, err := os.ReadFile(filepath.Join(targetPath, "options.txt"))
	if err != nil || string(gotOpt) != "restored" {
		t.Fatalf("expected restored options.txt, got %q (err=%v)", string(gotOpt), err)
	}
	gotLog, err := os.ReadFile(filepath.Join(targetPath, "logs", "latest.log"))
	if err != nil || string(gotLog) != "keep-log" {
		t.Fatalf("expected logs/latest.log to be preserved after swapRestoreSnapshot, got %q (err=%v)", string(gotLog), err)
	}
	gotLock, err := os.ReadFile(filepath.Join(targetPath, "saves", "world", "session.lock"))
	if err != nil || string(gotLock) != "keep-lock" {
		t.Fatalf("expected nested session.lock to be preserved after swapRestoreSnapshot, got %q (err=%v)", string(gotLock), err)
	}
}

type mockTimestampProvider struct {
	*mockStagedProvider
	listCalled atomic.Int32
	timestamps map[string]time.Time
}

func (m *mockTimestampProvider) ListFiles(s string) ([]providers.CloudFile, error) {
	m.listCalled.Add(1)
	return m.mockStagedProvider.ListFiles(s)
}

func (m *mockTimestampProvider) GetFileModifiedTime(fileID string) (time.Time, error) {
	t, ok := m.timestamps[fileID]
	if !ok {
		return time.Time{}, providers.ErrNotFound
	}
	return t, nil
}

func TestFetchServerTimeUsesFileTimestampFetcherWithoutListFiles(t *testing.T) {
	t1 := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 15, 12, 30, 0, 0, time.UTC)
	p := &mockTimestampProvider{
		mockStagedProvider: newMockStagedProvider(),
		timestamps: map[string]time.Time{
			"zip-1":  t1,
			"json-2": t2,
		},
	}
	opts := retry.Options{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond, Label: "test-ts"}

	got := fetchServerTimeForInstance(p, opts, "Pack", "zip-1", "json-2")
	if !got.Equal(t2) {
		t.Fatalf("expected max timestamp %v, got %v", t2, got)
	}
	if p.listCalled.Load() != 0 {
		t.Fatalf("expected ListFiles NOT to be called when FileTimestampFetcher succeeds, got %d calls", p.listCalled.Load())
	}
}

type stubMetaListProvider struct {
	stubDeleteProvider
	dlCalls atomic.Int32
}

func (s *stubMetaListProvider) DownloadJSON(id string, target any) error {
	s.dlCalls.Add(1)
	return nil
}

func TestHandleCloudListCachesEmptyIconMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HORIZON_DATA_DIR", tmpDir)

	modTime := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	localMetaPath := paths.GetMetaPath("NoIconPack")
	_ = os.MkdirAll(filepath.Dir(localMetaPath), 0755)
	_ = os.WriteFile(localMetaPath, []byte(`{"realName":"NoIconPack","loader":"fabric"}`), 0644)
	_ = os.Chtimes(localMetaPath, modTime, modTime)

	p := &stubMetaListProvider{}
	cloudIndex := map[string]providers.CloudFile{
		"GensHorizon_Backup_NoIconPack.zip":       {ID: "b1", Name: "GensHorizon_Backup_NoIconPack.zip", Size: 100, ModifiedTime: modTime},
		"GensHorizon_Meta_NoIconPack.json":        {ID: "m1", Name: "GensHorizon_Meta_NoIconPack.json", Size: 40, ModifiedTime: modTime},
	}

	handleCloudList(cloudIndex, p)
	if p.dlCalls.Load() != 0 {
		t.Fatalf("expected cached metadata without iconData to skip DownloadJSON, got %d calls", p.dlCalls.Load())
	}
}
