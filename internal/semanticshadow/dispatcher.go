package semanticshadow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultQueueCapacity = 64
	defaultExportTimeout = 250 * time.Millisecond
	maxResponseBytes     = 1
)

type Exporter interface {
	Export(context.Context, Graph) error
}

type observation struct {
	turnDigest string
	signals    Signals
}

type Dispatcher struct {
	exporter Exporter
	queue    chan observation
	done     chan struct{}
	once     sync.Once
}

func NewDispatcher(exporter Exporter) (*Dispatcher, error) {
	if exporter == nil {
		return nil, errors.New("semantic shadow exporter is required")
	}
	d := &Dispatcher{
		exporter: exporter,
		queue:    make(chan observation, defaultQueueCapacity),
		done:     make(chan struct{}),
	}
	go d.run()
	return d, nil
}

// Observe is wait-free with respect to network and graph processing. A full
// queue drops the shadow observation and can never delay the voice response.
func (d *Dispatcher) Observe(turnDigest string, signals Signals) bool {
	if d == nil {
		return false
	}
	select {
	case <-d.done:
		return false
	default:
	}
	select {
	case d.queue <- observation{turnDigest: turnDigest, signals: signals}:
		return true
	default:
		return false
	}
}

func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.once.Do(func() { close(d.done) })
}

func (d *Dispatcher) run() {
	for {
		select {
		case <-d.done:
			return
		case item := <-d.queue:
			graph, err := Build(item.turnDigest, item.signals)
			if err != nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), defaultExportTimeout)
			_ = d.exporter.Export(ctx, graph)
			cancel()
		}
	}
}

type HTTPExporter struct {
	client   *http.Client
	endpoint string
}

func NewHTTPExporter(client *http.Client, serviceURL string) (*HTTPExporter, error) {
	parsed, err := url.Parse(serviceURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("semantic shadow service URL is invalid")
	}
	if client == nil {
		return nil, errors.New("semantic shadow authenticated HTTP client is required")
	}
	return &HTTPExporter{
		client:   client,
		endpoint: strings.TrimRight(serviceURL, "/") + "/v1/shadow/graphs",
	}, nil
}

func (e *HTTPExporter) Export(ctx context.Context, graph Graph) error {
	body, err := CanonicalJSON(graph)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode != http.StatusNoContent || len(responseBody) != 0 {
		return errors.New("semantic shadow service rejected graph")
	}
	return nil
}
