package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// mcpWarmProxy makes slow, stateless HTTP MCP discovery available within ACP's
// 250 ms startup window. It serves cached initialize and tools/list responses
// locally, while forwarding every tool invocation to the upstream MCP server.
type mcpWarmProxy struct {
	client *http.Client

	mu        sync.Mutex
	server    *http.Server
	listener  net.Listener
	upstreams map[string]*mcpHTTPUpstream
}

type mcpHTTPUpstream struct {
	url     string
	headers http.Header

	mu         sync.RWMutex
	initialize json.RawMessage
	tools      json.RawMessage
}

func newMCPWarmProxy() *mcpWarmProxy {
	return &mcpWarmProxy{
		client:    &http.Client{Timeout: 15 * time.Second},
		upstreams: make(map[string]*mcpHTTPUpstream),
	}
}
func (p *mcpWarmProxy) Close() error {
	p.mu.Lock()
	server := p.server
	p.server = nil
	p.listener = nil
	p.upstreams = make(map[string]*mcpHTTPUpstream)
	p.mu.Unlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func (p *mcpWarmProxy) warmHTTPServers(ctx context.Context, servers []any) ([]any, error) {
	proxied := make([]any, len(servers))
	copy(proxied, servers)
	for i, raw := range servers {
		server, ok := raw.(map[string]any)
		if !ok || server["type"] != "http" {
			continue
		}
		url, _ := server["url"].(string)
		if url == "" {
			continue
		}
		upstream := &mcpHTTPUpstream{url: url, headers: nameValueHeaders(server["headers"])}
		cached, err := p.warm(ctx, upstream)
		if err != nil {
			return nil, fmt.Errorf("warm HTTP MCP %q: %w", server["name"], err)
		}
		if !cached {
			continue
		}
		proxyURL, err := p.register(upstream)
		if err != nil {
			return nil, err
		}
		clone := make(map[string]any, len(server))
		for key, value := range server {
			clone[key] = value
		}
		clone["url"] = proxyURL
		proxied[i] = clone
	}
	return proxied, nil
}

func (p *mcpWarmProxy) warm(ctx context.Context, upstream *mcpHTTPUpstream) (bool, error) {
	init, sessionID, err := p.request(ctx, upstream, json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"omp-im","version":"1.0"}}}`))
	if err != nil {
		return false, err
	}
	// Reusing a server-assigned session ID would send independent ACP sessions
	// through one MCP session. Keep stateful servers on their original URL.
	if sessionID != "" {
		return false, nil
	}
	tools, _, err := p.request(ctx, upstream, json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`))
	if err != nil {
		return false, err
	}
	upstream.mu.Lock()
	upstream.initialize = init
	upstream.tools = tools
	upstream.mu.Unlock()
	return true, nil
}

func (p *mcpWarmProxy) register(upstream *mcpHTTPUpstream) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server == nil {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", fmt.Errorf("listen for MCP warm proxy: %w", err)
		}
		p.listener = listener
		p.server = &http.Server{Handler: http.HandlerFunc(p.serveHTTP)}
		go func() { _ = p.server.Serve(listener) }()
	}
	headerJSON, _ := json.Marshal(upstream.headers)
	keyBytes := sha256.Sum256(append([]byte(upstream.url), headerJSON...))
	key := hex.EncodeToString(keyBytes[:8])
	p.upstreams[key] = upstream
	return "http://" + p.listener.Addr().String() + "/mcp/" + key, nil
}

func (p *mcpWarmProxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "MCP warm proxy accepts POST only", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Path[len("/mcp/"):]
	p.mu.Lock()
	upstream := p.upstreams[key]
	p.mu.Unlock()
	if upstream == nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read MCP request", http.StatusBadRequest)
		return
	}
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
		return
	}
	upstream.mu.RLock()
	cached := json.RawMessage(nil)
	switch request.Method {
	case "initialize":
		cached = upstream.initialize
	case "tools/list":
		cached = upstream.tools
	}
	upstream.mu.RUnlock()
	if len(cached) > 0 {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustRPCResult(request.ID, cached))
		return
	}
	response, headers, err := p.forward(r.Context(), upstream, body, r.Header)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	copyHeaders(w.Header(), headers)
	_, _ = w.Write(response)
}

func (p *mcpWarmProxy) request(ctx context.Context, upstream *mcpHTTPUpstream, body json.RawMessage) (json.RawMessage, string, error) {
	response, headers, err := p.forward(ctx, upstream, body, nil)
	if err != nil {
		return nil, "", err
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(response, &rpc); err != nil {
		return nil, "", fmt.Errorf("decode MCP response: %w", err)
	}
	if rpc.Error != nil {
		return nil, "", rpc.Error
	}
	if len(rpc.Result) == 0 {
		return nil, "", fmt.Errorf("MCP response has no result")
	}
	return rpc.Result, headers.Get("Mcp-Session-Id"), nil
}

func (p *mcpWarmProxy) forward(ctx context.Context, upstream *mcpHTTPUpstream, body []byte, requestHeaders http.Header) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	copyHeaders(req.Header, upstream.headers)
	copyHeaders(req.Header, requestHeaders)
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if req.Header.Get("MCP-Protocol-Version") == "" {
		req.Header.Set("MCP-Protocol-Version", "2025-03-26")
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("MCP upstream returned %s: %s", response.Status, string(payload))
	}
	return payload, response.Header, nil
}

func nameValueHeaders(raw any) http.Header {
	headers := make(http.Header)
	values, ok := raw.([]map[string]string)
	if !ok {
		return headers
	}
	for _, value := range values {
		headers.Set(value["name"], value["value"])
	}
	return headers
}

func mustRPCResult(id, result json.RawMessage) []byte {
	response := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: result}
	payload, _ := json.Marshal(response)
	return payload
}

func copyHeaders(destination, source http.Header) {
	for name, values := range source {
		for _, value := range values {
			destination.Add(name, value)
		}
	}
}
