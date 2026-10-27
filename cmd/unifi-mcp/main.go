// Command unifi-mcp is a Model Context Protocol server for Ubiquiti UniFi Network.
//
//	unifi-mcp              serve MCP (stdio, or Streamable HTTP with MCP_TRANSPORT=http)
//	unifi-mcp healthcheck  check the HTTP server is up (for Docker HEALTHCHECK)
//	unifi-mcp version      print the version
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mattoddie/unifi-mcp/internal/config"
	"github.com/mattoddie/unifi-mcp/internal/httpserver"
	"github.com/mattoddie/unifi-mcp/internal/tools"
	"github.com/mattoddie/unifi-mcp/internal/unifi"
)

func main() {
	// Logs go to stderr: stdout is the MCP channel in stdio mode.
	log.SetFlags(0)
	log.SetOutput(os.Stderr)
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "healthcheck":
		err = healthcheck()
	case "version", "--version", "-v":
		fmt.Println(tools.Version)
	case "help", "--help", "-h":
		fmt.Print(usage)
	default:
		err = fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
	if err != nil {
		log.Printf("unifi-mcp: %v", err)
		os.Exit(1)
	}
}

const usage = `Usage: unifi-mcp [command]

Commands:
  serve        Serve MCP (default). stdio, or Streamable HTTP with MCP_TRANSPORT=http
  healthcheck  Exit 0 if the HTTP server answers /health (for Docker HEALTHCHECK)
  version      Print the version

Configuration is by environment variable; see docs/configuration.md.
`

func serve() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}
	client, err := unifi.New(cfg.Unifi)
	if err != nil {
		return err
	}
	defer client.Close()

	auth := "username/password"
	if cfg.Unifi.APIKey != "" {
		auth = "API key"
	}
	writes := "disabled"
	if cfg.Tools.AllowWrites {
		writes = "ENABLED"
	}
	if cfg.Tools.AllowDeletes {
		writes += " (with deletes)"
	}
	secrets := "redacted"
	if cfg.Tools.AllowSecrets {
		secrets = "ALLOWED"
	}
	audit := "off"
	if cfg.Tools.AuditLog != "" {
		audit = cfg.Tools.AuditLog
	}
	log.Printf(`unifi-mcp %s: controller %s, site "%s", auth %s, writes %s, secrets %s, audit log %s, transport %s`, tools.Version, cfg.Unifi.URL, cfg.Unifi.Site, auth, writes, secrets, audit, cfg.Transport)
	for _, warning := range []string{config.InsecureTLSWarning(cfg.Unifi), config.PlainHTTPWarning(cfg.Unifi)} {
		if warning == "" {
			continue
		}
		log.Printf("unifi-mcp: WARNING: %s", warning)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := tools.NewServer(client, cfg.Tools, nil)
	if cfg.Transport == "stdio" {
		err := server.Run(ctx, &mcp.StdioTransport{})
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	if err := httpserver.Check(cfg.HTTP); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", httpserver.Addr(cfg.HTTP))
	if err != nil {
		return err
	}
	// No WriteTimeout: tool calls can legitimately take minutes (rolling upgrades, RF scans).
	srv := &http.Server{
		Handler:           httpserver.Handler(cfg.HTTP, server),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	return httpserver.Serve(srv, ln, cfg.HTTP)
}

func healthcheck() error {
	url, err := config.HealthcheckURL(os.LookupEnv)
	if err != nil || url == "" {
		return err
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %s", resp.Status)
	}
	return nil
}
