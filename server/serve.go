// Package server assembles what `verdict serve` runs: a Twirp endpoint, an MCP
// endpoint, and a health check over one HTTP listener. It also serves MCP over
// stdio, for a client that launches Verdict as a subprocess.
//
// The library has no dependency on this package. Everything here is a thin
// wrapper: a Go service that wants a decision engine imports pkg/verdict and
// never starts a server at all.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/twitchtv/twirp"

	"github.com/frankbardon/verdict/pkg/verdict"
	vmcp "github.com/frankbardon/verdict/server/mcp"
	"github.com/frankbardon/verdict/server/mcp/gosdk"
	vtwirp "github.com/frankbardon/verdict/server/twirp"
)

// Options configure a Server.
type Options struct {
	// Listen is the address to bind, e.g. ":7430".
	Listen string
	// TwirpPath is the prefix the Twirp handler is mounted under. Defaults to
	// "/twirp".
	TwirpPath string
	// MCPEnabled mounts the MCP endpoint at /mcp.
	MCPEnabled bool
	// MCPAllowLoad exposes the model-loading MCP tool.
	MCPAllowLoad bool
	// ModelRoot bounds LoadModel's path field. Empty forbids loading by path.
	ModelRoot string
	// ReadTimeout and WriteTimeout bound a single request.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// Version is reported by the health endpoint and to MCP clients.
	Version string
	// Logger receives request and lifecycle logs.
	Logger *slog.Logger
}

func (o *Options) defaults() {
	if o.Listen == "" {
		o.Listen = ":7430"
	}
	if o.TwirpPath == "" {
		o.TwirpPath = "/twirp"
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = 30 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 60 * time.Second
	}
	if o.Version == "" {
		o.Version = vmcp.DefaultVersion
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Server is the assembled HTTP surface.
type Server struct {
	opts   Options
	engine *verdict.Engine
	http   *http.Server
	mux    *http.ServeMux
}

// New builds a server over an engine.
func New(engine *verdict.Engine, opts Options) (*Server, error) {
	if engine == nil {
		return nil, fmt.Errorf("verdict/server: an engine is required")
	}
	opts.defaults()

	mux := http.NewServeMux()
	s := &Server{opts: opts, engine: engine, mux: mux}

	rpc := vtwirp.NewServer(engine, vtwirp.WithModelRoot(opts.ModelRoot))
	handler := vtwirp.NewEngineServer(rpc, twirp.WithServerPathPrefix(twirpPathPrefix(opts.TwirpPath)))
	mux.Handle(strings.TrimSuffix(opts.TwirpPath, "/")+"/", handler)

	if opts.MCPEnabled {
		if err := s.mountMCP(); err != nil {
			return nil, err
		}
	}

	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/readyz", s.health)

	s.http = &http.Server{
		Addr:              opts.Listen,
		Handler:           s.withLogging(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
	}
	return s, nil
}

// mountMCP attaches the MCP surface over streamable HTTP.
func (s *Server) mountMCP() error {
	srv, err := newMCPServer(s.engine, s.opts)
	if err != nil {
		return err
	}
	handler := mcpsdk.NewStreamableHTTPHandler(
		func(*http.Request) *mcpsdk.Server { return srv }, nil)
	s.mux.Handle("/mcp", handler)
	s.mux.Handle("/mcp/", handler)
	return nil
}

// newMCPServer builds the MCP server both transports share. HTTP and stdio
// therefore expose exactly the same tools and resources — a client that behaves
// differently depending on how it reached Verdict is a debugging session nobody
// enjoys.
func newMCPServer(engine *verdict.Engine, opts Options) (*mcpsdk.Server, error) {
	impl := &mcpsdk.Implementation{Name: vmcp.DefaultServerName, Version: opts.Version}
	srv := mcpsdk.NewServer(impl, nil)
	cfg := vmcp.Config{
		ServerName: vmcp.DefaultServerName,
		Version:    opts.Version,
		AllowLoad:  opts.MCPAllowLoad,
	}
	if err := gosdk.Register(srv, engine, cfg); err != nil {
		return nil, err
	}
	return srv, nil
}

// ServeStdio runs the MCP surface over stdin and stdout until ctx is cancelled
// or the client disconnects. It is what an MCP client that launches Verdict as
// a subprocess talks to.
//
// Nothing may be written to stdout but protocol frames: stdout *is* the
// transport. Anything a caller wants to log goes to stderr, which is why
// Options.Logger must not be writing to stdout when this is used.
func ServeStdio(ctx context.Context, engine *verdict.Engine, opts Options) error {
	return serveMCP(ctx, engine, opts, &mcpsdk.StdioTransport{})
}

// serveMCP is ServeStdio with the transport injected, so the surface can be
// driven end to end in a test over an in-memory pipe rather than by spawning a
// subprocess and parsing its stdout.
func serveMCP(ctx context.Context, engine *verdict.Engine, opts Options, t mcpsdk.Transport) error {
	if engine == nil {
		return fmt.Errorf("verdict/server: an engine is required")
	}
	opts.defaults()
	srv, err := newMCPServer(engine, opts)
	if err != nil {
		return err
	}
	if err := srv.Run(ctx, t); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("verdict/server: serving MCP: %w", err)
	}
	return nil
}

// Handler exposes the assembled mux, so a host can mount Verdict inside its own
// HTTP server rather than running a second listener.
func (s *Server) Handler() http.Handler { return s.withLogging(s.mux) }

// ListenAndServe runs the server until ctx is cancelled, then shuts it down
// gracefully.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		return fmt.Errorf("verdict/server: listening on %s: %w", s.opts.Listen, err)
	}
	s.opts.Logger.Info("verdict listening",
		"addr", ln.Addr().String(),
		"twirp", s.opts.TwirpPath,
		"mcp", s.opts.MCPEnabled,
		"models", len(s.engine.ListModels()))

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.opts.Logger.Info("verdict shutting down")
		return s.http.Shutdown(shutdownCtx)
	}
}

// health answers the liveness and readiness probes. A Verdict server with no
// models loaded is live but not useful, so the payload says how many it has
// rather than only "ok".
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	models := s.engine.ListModels()
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": s.opts.Version,
		"models":  ids,
	})
}

// withLogging records one line per request. It deliberately logs the path and
// status only: a decision request body is the caller's data, and a server that
// logs it by default has quietly become a data store.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.opts.Logger.Debug("request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "duration", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// twirpPathPrefix normalises the configured mount point into the form the
// generated Twirp handler expects: a leading slash and no trailing one.
func twirpPathPrefix(p string) string {
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return "/twirp"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
