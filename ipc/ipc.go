package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

var stdoutMu sync.Mutex

type BaseMessage struct {
	Type      string `json:"type"`
	Message   string `json:"message,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
	Instance  string `json:"instance,omitempty"`
}

type ProgressMessage struct {
	Type     string `json:"type"`
	Step     string `json:"step"`
	Value    int    `json:"value"`
	Instance string `json:"instance,omitempty"`
}

type CheckResultMessage struct {
	Type    string   `json:"type"`
	Status  string   `json:"status"`
	Updates []string `json:"updates"`
}

type RichInstanceInfo struct {
	Name       string  `json:"name"`
	RealName   string  `json:"realName"`
	DeltaCount int     `json:"deltaCount"`
	SizeBytes  int64   `json:"sizeBytes"`
	LastBackup *string `json:"lastBackup"`
	IconData   *string `json:"iconData"`
	Loader     *string `json:"loader"`
}

type CloudListMessage struct {
	Type     string             `json:"type"`
	Data     []string           `json:"data"`
	RichData []RichInstanceInfo `json:"richData"`
}

type RollbackItem struct {
	Folder    string  `json:"folder"`
	Instance  string  `json:"instance"`
	Timestamp *string `json:"timestamp"`
}

type RollbackListMessage struct {
	Type string         `json:"type"`
	Data []RollbackItem `json:"data"`
}

type QuotaInstance struct {
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
	DeltaCount int    `json:"deltaCount"`
}

type QuotaMessage struct {
	Type        string          `json:"type"`
	UsedBytes   int64           `json:"usedBytes"`
	TotalBytes  int64           `json:"totalBytes"`
	HorizonUsed int64           `json:"horizonUsed"`
	Instances   []QuotaInstance `json:"instances"`
}

// Send écrit n'importe quel objet JSON sur stdout (thread-safe)
func Send(payload any) {
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	b, err := json.Marshal(payload)
	if err == nil {
		fmt.Fprintf(os.Stdout, "%s\n", b)
	}
}

func SendProgress(step string, value int, instance string) {
	Send(ProgressMessage{
		Type:     "PROGRESS",
		Step:     step,
		Value:    value,
		Instance: instance,
	})
}

func SendSuccess(instance string, message string) {
	Send(BaseMessage{
		Type:     "SUCCESS",
		Instance: instance,
		Message:  message,
	})
}

func SendError(errorCode string, message string, instance string) {
	Send(BaseMessage{
		Type:      "ERROR",
		ErrorCode: errorCode,
		Message:   message,
		Instance:  instance,
	})
}

func SendOffline(message string) {
	Send(map[string]any{
		"type":    "OFFLINE",
		"status":  "OFFLINE",
		"message": message,
	})
}

func SendInfo(message string) {
	Send(BaseMessage{
		Type:    "INFO",
		Message: message,
	})
}

// Logf écrit des messages de log ou d'erreur sur stderr afin de ne pas corrompre le stream JSON sur stdout
func Logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

