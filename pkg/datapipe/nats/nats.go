package nats

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/songquanpeng/one-api/pkg/datapipe"
)

// Message types for NATS communication
const (
	MsgTypeHeader = "header"
	MsgTypeChunk  = "chunk"
	MsgTypeEOF    = "eof"
)

// Default timeout for NATS operations
const (
	DefaultTimeout     = 30 * time.Second
	DefaultChunkSize   = 32 * 1024 // 32KB chunks
	DefaultReadTimeout = 5 * time.Minute
)

// RequestMessage represents a serialized HTTP request sent over NATS
type RequestMessage struct {
	Method  string              `json:"method"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body,omitempty"` // Base64 encoded
}

// ResponseMessage represents a response message from NATS
type ResponseMessage struct {
	Type       string              `json:"type"`
	StatusCode int                 `json:"status_code,omitempty"`
	Headers    map[string][]string `json:"headers,omitempty"`
	Data       string              `json:"data,omitempty"` // Base64 encoded for chunks
}

// NatsPipe implements datapipe.DataPipe interface using NATS
type NatsPipe struct {
	nc           *nats.Conn
	config       map[string]string
	maxBodyBytes int64
}

// NatsTransport implements http.RoundTripper interface
type NatsTransport struct {
	nc            *nats.Conn
	headerTimeout time.Duration
	chunkTimeout  time.Duration
	maxBodyBytes  int64
}

type cancelReadCloser struct {
	rc     io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Read(p []byte) (n int, err error) { return c.rc.Read(p) }
func (c *cancelReadCloser) Close() error {
	c.cancel()
	return c.rc.Close()
}

func init() {
	datapipe.Register(&NatsPipe{})
}

// Name returns the identifier for this DataPipe
func (p *NatsPipe) Name() string {
	return "nats"
}

// Init establishes NATS connection using the provided config
func (p *NatsPipe) Init(config map[string]string) error {
	p.config = config

	if raw, ok := config["max_body_bytes"]; ok && raw != "" {
		maxBytes, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || maxBytes < 0 {
			return fmt.Errorf("invalid max_body_bytes: %s", raw)
		}
		p.maxBodyBytes = maxBytes
	}

	natsURL, ok := config["nats_url"]
	if !ok {
		return fmt.Errorf("nats_url is required in config")
	}

	nc, err := nats.Connect(natsURL,
		nats.Timeout(DefaultTimeout),
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(5),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			fmt.Printf("[NatsPipe] Disconnected from NATS: %v\n", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			fmt.Printf("[NatsPipe] Reconnected to NATS at %s\n", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to NATS: %w", err)
	}

	p.nc = nc
	return nil
}

// GetTransport returns the custom HTTP transport
func (p *NatsPipe) GetTransport() http.RoundTripper {
	return &NatsTransport{
		nc:            p.nc,
		headerTimeout: DefaultTimeout,
		chunkTimeout:  DefaultReadTimeout,
		maxBodyBytes:  p.maxBodyBytes,
	}
}

// Listen starts listening for requests and forwards them to the internal relay server.
// This method blocks and should be run in a goroutine or as the main loop.
// The targetBaseURL parameter is ignored; internal_url from config is used instead.
func (p *NatsPipe) Listen(targetBaseURL string) error {
	subject, ok := p.config["nats_subject"]
	if !ok {
		return fmt.Errorf("nats_subject is required in config for listening")
	}

	// Get internal relay URL from config (set by forwarder mode)
	internalURL, ok := p.config["internal_url"]
	if !ok {
		internalURL = "http://127.0.0.1:13000"
	}

	fmt.Printf("[NatsPipe] Starting listener on subject: %s, forwarding to internal relay: %s\n", subject, internalURL)

	// Subscribe to the subject with a handler
	queueGroup := strings.TrimSpace(p.config["nats_queue"])
	var err error
	if queueGroup != "" {
		_, err = p.nc.QueueSubscribe(subject, queueGroup, func(msg *nats.Msg) {
			go p.handleRequestToInternalRelay(msg, internalURL)
		})
	} else {
		_, err = p.nc.Subscribe(subject, func(msg *nats.Msg) {
			go p.handleRequestToInternalRelay(msg, internalURL)
		})
	}
	if err != nil {
		return fmt.Errorf("failed to subscribe to subject %s: %w", subject, err)
	}

	fmt.Printf("[NatsPipe] Successfully subscribed to %s, waiting for requests...\n", subject)

	// Block forever (or until connection is closed)
	select {}
}

// handleRequestToInternalRelay processes an incoming NATS request and forwards it to the internal relay server.
// This preserves the original request path and lets the internal relay handle routing based on model.
func (p *NatsPipe) handleRequestToInternalRelay(msg *nats.Msg, internalURL string) {
	replySubject := msg.Reply
	if replySubject == "" {
		fmt.Println("[NatsPipe] Warning: received request with no reply subject, skipping")
		return
	}

	// Deserialize the request
	var reqMsg RequestMessage
	if err := json.Unmarshal(msg.Data, &reqMsg); err != nil {
		p.sendError(replySubject, fmt.Sprintf("failed to unmarshal request: %v", err))
		return
	}

	// Parse the original URL to extract path and query
	parsedURL, err := url.Parse(reqMsg.URL)
	if err != nil {
		p.sendError(replySubject, fmt.Sprintf("failed to parse request URL: %v", err))
		return
	}

	// Build target URL: internal relay base + original path + query
	targetURL := internalURL + parsedURL.Path
	if parsedURL.RawQuery != "" {
		targetURL += "?" + parsedURL.RawQuery
	}

	var body io.Reader
	if reqMsg.Body != "" {
		bodyBytes, err := base64.StdEncoding.DecodeString(reqMsg.Body)
		if err != nil {
			p.sendError(replySubject, fmt.Sprintf("failed to decode body: %v", err))
			return
		}
		body = bytes.NewReader(bodyBytes)
	}

	httpReq, err := http.NewRequest(reqMsg.Method, targetURL, body)
	if err != nil {
		p.sendError(replySubject, fmt.Sprintf("failed to create request: %v", err))
		return
	}

	// Restore headers
	for key, values := range reqMsg.Headers {
		for _, value := range values {
			httpReq.Header.Add(key, value)
		}
	}

	// Create HTTP client with extended timeout for streaming
	client := &http.Client{Timeout: DefaultReadTimeout}

	// Execute the request to internal relay
	resp, err := client.Do(httpReq)
	if err != nil {
		p.sendError(replySubject, fmt.Sprintf("request to internal relay failed: %v", err))
		return
	}
	defer resp.Body.Close()

	// Send header message first
	headerMsg := ResponseMessage{
		Type:       MsgTypeHeader,
		StatusCode: resp.StatusCode,
		Headers:    make(map[string][]string),
	}
	for key, values := range resp.Header {
		headerMsg.Headers[key] = values
	}
	headerData, _ := json.Marshal(headerMsg)
	if err := p.nc.Publish(replySubject, headerData); err != nil {
		fmt.Printf("[NatsPipe] Failed to send header: %v\n", err)
		return
	}

	// Stream the response body in chunks
	buffer := make([]byte, DefaultChunkSize)
	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			chunkMsg := ResponseMessage{
				Type: MsgTypeChunk,
				Data: base64.StdEncoding.EncodeToString(buffer[:n]),
			}
			chunkData, _ := json.Marshal(chunkMsg)
			if err := p.nc.Publish(replySubject, chunkData); err != nil {
				fmt.Printf("[NatsPipe] Failed to send chunk: %v\n", err)
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Printf("[NatsPipe] Error reading response body: %v\n", err)
			break
		}
	}

	// Send EOF message
	eofMsg := ResponseMessage{Type: MsgTypeEOF}
	eofData, _ := json.Marshal(eofMsg)
	if err := p.nc.Publish(replySubject, eofData); err != nil {
		fmt.Printf("[NatsPipe] Failed to send EOF: %v\n", err)
	}
}

// sendError sends an error response back to the caller
func (p *NatsPipe) sendError(replySubject, errMsg string) {
	fmt.Printf("[NatsPipe] Sending error: %s\n", errMsg)
	headerMsg := ResponseMessage{
		Type:       MsgTypeHeader,
		StatusCode: http.StatusInternalServerError,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
	}
	headerData, _ := json.Marshal(headerMsg)
	_ = p.nc.Publish(replySubject, headerData)

	// Send error as chunk
	chunkMsg := ResponseMessage{
		Type: MsgTypeChunk,
		Data: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"error": %q}`, errMsg))),
	}
	chunkData, _ := json.Marshal(chunkMsg)
	_ = p.nc.Publish(replySubject, chunkData)

	// Send EOF
	eofMsg := ResponseMessage{Type: MsgTypeEOF}
	eofData, _ := json.Marshal(eofMsg)
	_ = p.nc.Publish(replySubject, eofData)
}

// RoundTrip implements http.RoundTripper interface
func (t *NatsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Serialize the HTTP request
	reqMsg, err := t.serializeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request: %w", err)
	}

	reqData, err := json.Marshal(reqMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request message: %w", err)
	}

	// Create reply inbox for receiving response
	replyInbox := t.nc.NewRespInbox()

	// For nats:// URLs, the host is the subject.
	targetSubject := t.buildTargetSubject(req.URL.Host)
	if targetSubject == "" {
		return nil, fmt.Errorf("empty NATS subject")
	}

	// Subscribe to reply inbox before publishing
	sub, err := t.nc.SubscribeSync(replyInbox)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to reply inbox: %w", err)
	}

	// Publish request with reply subject
	if err := t.nc.PublishRequest(targetSubject, replyInbox, reqData); err != nil {
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("failed to publish request: %w", err)
	}

	// Wait for header message (first response)
	msg, err := sub.NextMsg(t.headerTimeout)
	if err != nil {
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("timeout waiting for response header: %w", err)
	}

	// Parse header message
	var headerMsg ResponseMessage
	if err := json.Unmarshal(msg.Data, &headerMsg); err != nil {
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("failed to unmarshal header message: %w", err)
	}

	if headerMsg.Type != MsgTypeHeader {
		_ = sub.Unsubscribe()
		return nil, fmt.Errorf("expected header message, got: %s", headerMsg.Type)
	}

	// Build HTTP response from header
	resp := &http.Response{
		StatusCode: headerMsg.StatusCode,
		Status:     fmt.Sprintf("%d %s", headerMsg.StatusCode, http.StatusText(headerMsg.StatusCode)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Request:    req,
	}

	for key, values := range headerMsg.Headers {
		for _, value := range values {
			resp.Header.Add(key, value)
		}
	}

	resp.Body = t.createStreamingBody(req.Context(), sub)
	return resp, nil
}

// serializeRequest converts http.Request to RequestMessage
func (t *NatsTransport) serializeRequest(req *http.Request) (*RequestMessage, error) {
	msg := &RequestMessage{
		Method:  req.Method,
		URL:     req.URL.String(),
		Headers: make(map[string][]string),
	}

	// Copy headers
	for key, values := range req.Header {
		msg.Headers[key] = values
	}

	// Read and encode body if present
	if req.Body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
		_ = req.Body.Close()
		if t.maxBodyBytes > 0 && int64(len(bodyBytes)) > t.maxBodyBytes {
			return nil, fmt.Errorf("%w: %d > %d", datapipe.ErrBodyTooLarge, len(bodyBytes), t.maxBodyBytes)
		}
		msg.Body = base64.StdEncoding.EncodeToString(bodyBytes)
	}

	return msg, nil
}

// buildTargetSubject constructs NATS subject from host.
// For nats:// requests, the host is already a subject (e.g., "llm.requests").
func (t *NatsTransport) buildTargetSubject(host string) string {
	return strings.TrimSpace(host)
}

func (t *NatsTransport) createStreamingBody(parentCtx context.Context, sub *nats.Subscription) io.ReadCloser {
	ctx, cancel := context.WithCancel(parentCtx)
	pr, pw := io.Pipe()

	go func() {
		defer cancel()
		defer sub.Unsubscribe()
		defer pw.Close()

		for {
			select {
			case <-ctx.Done():
				_ = pw.CloseWithError(ctx.Err())
				return
			default:
			}

			msg, err := sub.NextMsg(t.chunkTimeout)
			if err != nil {
				if err == nats.ErrTimeout {
					continue
				}
				_ = pw.CloseWithError(err)
				return
			}

			var respMsg ResponseMessage
			if err := json.Unmarshal(msg.Data, &respMsg); err != nil {
				_ = pw.CloseWithError(fmt.Errorf("failed to unmarshal chunk: %w", err))
				return
			}

			switch respMsg.Type {
			case MsgTypeChunk:
				data, err := base64.StdEncoding.DecodeString(respMsg.Data)
				if err != nil {
					_ = pw.CloseWithError(fmt.Errorf("failed to decode chunk data: %w", err))
					return
				}
				if _, err := pw.Write(data); err != nil {
					return
				}

			case MsgTypeEOF:
				return

			default:
				_ = pw.CloseWithError(fmt.Errorf("unexpected message type: %s", respMsg.Type))
				return
			}
		}
	}()

	return &cancelReadCloser{rc: pr, cancel: cancel}
}
