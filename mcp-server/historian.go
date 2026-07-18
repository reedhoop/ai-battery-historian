package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// HistorianClient posts bugreports to a running Battery Historian HTTP service.
type HistorianClient struct {
	BaseURL string
	HTTP    *http.Client
}

func NewHistorianClient(base string) *HistorianClient {
	// FR-14: a hard timeout bounds a stuck/slow Historian backend so an MCP
	// call can never hang forever. 5 minutes covers large bugreports.
	return &HistorianClient{BaseURL: base, HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// postBugreports uploads one or two bugreport files as multipart and returns
// the raw uploadResponseCompare JSON body.
func (c *HistorianClient) postBugreports(fieldNames, paths []string) ([]byte, error) {
	if len(fieldNames) != len(paths) {
		return nil, fmt.Errorf("field/path count mismatch")
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		part, err := w.CreateFormFile(fieldNames[i], filepath.Base(p))
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/historian/", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("historian returned %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// ---- Types mirroring the Historian JSON response (field names = json tags) ----

type UploadResponseCompare struct {
	UploadResponse  []UploadResponse `json:"UploadResponse"`
	CombinedCheckin CombinedCheckin  `json:"combinedCheckin"`
	UsingComparison bool             `json:"usingComparison"`
}

type UploadResponse struct {
	SDKVersion     int             `json:"sdkVersion"`
	AppStats       []AppStat       `json:"appStats"`
	BatteryStats   json.RawMessage `json:"batteryStats"`
	DeviceCapacity float64         `json:"deviceCapacity"`
	HistogramStats json.RawMessage `json:"histogramStats"`
	CriticalError  string          `json:"criticalError"`
	Note           string          `json:"note"`
	FileName       string          `json:"fileName"`
	Location       string          `json:"location"`
	IsDiff         bool            `json:"isDiff"`
}

type AppStat struct {
	DevicePowerPrediction float64         `json:"devicePowerPrediction"`
	CPUPowerPrediction    float64         `json:"cpuPowerPrediction"`
	RawStats              json.RawMessage `json:"rawStats"`
}

// appKey peeks name/uid out of the (opaque) RawStats JSON.
type appKey struct {
	Name string `json:"name"`
	UID  int32  `json:"uid"`
}

type CombinedCheckin struct {
	UserspaceWakelocksCombined   []json.RawMessage `json:"UserspaceWakelocksCombined"`
	KernelWakelocksCombined      []json.RawMessage `json:"KernelWakelocksCombined"`
	SyncTasksCombined            []json.RawMessage `json:"SyncTasksCombined"`
	WakeupReasonsCombined        []json.RawMessage `json:"WakeupReasonsCombined"`
	TopMobileActiveAppsCombined  []json.RawMessage `json:"TopMobileActiveAppsCombined"`
	TopMobileTrafficAppsCombined []json.RawMessage `json:"TopMobileTrafficAppsCombined"`
	TopWifiTrafficAppsCombined   []json.RawMessage `json:"TopWifiTrafficAppsCombined"`
	DevicePowerEstimatesCombined []json.RawMessage `json:"DevicePowerEstimatesCombined"`
	AppWakeupsCombined           []json.RawMessage `json:"AppWakeupsCombined"`
	ANRAndCrashCombined          []json.RawMessage `json:"ANRAndCrashCombined"`
	CPUUsageCombined             []json.RawMessage `json:"CPUUsageCombined"`
}

// parseAnalyze parses a single-bugreport Historian response.
func parseAnalyze(body []byte) (*UploadResponseCompare, error) {
	var urc UploadResponseCompare
	if err := json.Unmarshal(body, &urc); err != nil {
		return nil, fmt.Errorf("parse historian response: %w", err)
	}
	return &urc, nil
}
