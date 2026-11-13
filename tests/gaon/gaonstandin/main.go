// Command gaonstandin serves tests/gaonfake on a port, over mutual TLS,
// for the pair run (tests/pairrun/pair-run.sh). It is what the run's gaon
// API is: an in-memory map of the three kinds with gaon's own
// validation, and nothing behind it. It starts no microVM; the run
// states a MicroVM's status through the admin path below, as a Node
// Agent would write it.
//
// The admin path is not gaon's: PUT /_admin/microvms/{name}/status
// takes a MicroVMStatus and stores it. It answers on the same
// listener, so it needs the same client certificate.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/garamsh/gaon/lib/api"

	"github.com/garamsh/garam-agent-operator/tests/gaon/gaonfake"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gaonstandin:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	var addr, addrFile, certFile, keyFile, caFile string
	f := flag.NewFlagSet("gaonstandin", flag.ContinueOnError)
	f.StringVar(&addr, "addr", "127.0.0.1:0", "the address to listen on; port 0 takes a free one")
	f.StringVar(&addrFile, "addr-file", "", "a file the listening address is written to")
	f.StringVar(&certFile, "cert-file", "", "the server certificate chain")
	f.StringVar(&keyFile, "key-file", "", "the server certificate's key")
	f.StringVar(&caFile, "client-ca-file", "", "the CA the client certificate must chain to")
	if err := f.Parse(args); err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("server certificate: %w", err)
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("client CA file holds no certificate")
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	fake := gaonfake.NewServer()
	mux := http.NewServeMux()
	mux.Handle("/", fake.Handler())
	mux.HandleFunc("PUT /_admin/microvms/{name}/status", adminStatus(fake))
	srv := &http.Server{
		Handler:           logged(log, mux),
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{pair},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS12,
		},
	}
	if addrFile != "" {
		if err := os.WriteFile(addrFile, []byte(listener.Addr().String()+"\n"), 0o600); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("gaonstandin serving", "addr", listener.Addr().String())
	if err := srv.ServeTLS(listener, "", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// adminStatus stores the MicroVMStatus in the body on the named
// MicroVM, past the validation a client's write goes through.
func adminStatus(fake *gaonfake.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var status api.MicroVMStatus
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&status); err != nil {
			http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := fake.WriteStatus(r.PathValue("name"), status); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// logged writes one line per request: method, path, the client
// certificate's name and the answer's status. It never writes a body,
// so a Secret's data is not in the log.
func logged(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		client := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			client = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		log.Info("request", "method", r.Method, "path", r.URL.Path, "client", client, "status", rec.status)
	})
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
