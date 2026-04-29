package daemoncmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"symterm/internal/admin"
	"symterm/internal/config"
	"symterm/internal/diagnostic"
	"symterm/internal/setup"
)

type daemonShutdownStep struct {
	name string
	run  func() error
}

type daemonShutdownCoordinator struct {
	once  sync.Once
	trace traceFunc
	steps []daemonShutdownStep
	err   error
}

func Run(ctx context.Context, cfg config.DaemonConfig, stdin io.Reader, stdout io.Writer) error {
	cfg = withAdminDefaults(cfg)
	tracef(cfg.Tracef, "daemon start projects_root=%q ssh_listen_addr=%q admin_socket=%q admin_web=%q unsafe_no_fuse=%t", cfg.ProjectsRoot, cfg.SSHListenAddr, cfg.AdminSocketPath, cfg.AdminWebAddr, cfg.AllowUnsafeNoFuse)
	container, err := setup.NewDaemonContainer(ctx, cfg)
	if err != nil {
		tracef(cfg.Tracef, "daemon container init failed error=%v", err)
		return err
	}
	tracef(cfg.Tracef, "daemon container ready")
	service := container.ClientService
	adminSocketListener, err := admin.ListenAdminSocket(cfg.AdminSocketPath)
	if err != nil {
		tracef(cfg.Tracef, "admin socket listen failed path=%q error=%v", cfg.AdminSocketPath, err)
		return err
	}
	defer adminSocketListener.Close()
	tracef(cfg.Tracef, "admin socket listening path=%q", cfg.AdminSocketPath)

	var adminHTTPListener net.Listener
	if strings.TrimSpace(cfg.AdminWebAddr) != "" {
		adminHTTPListener, err = net.Listen("tcp", cfg.AdminWebAddr)
		if normalizedErr := normalizeAdminHTTPServeError(container.AdminService, err); normalizedErr == nil && err != nil {
			tracef(cfg.Tracef, "admin http disabled addr=%q error=%v", cfg.AdminWebAddr, err)
			adminHTTPListener = nil
		} else if err != nil {
			tracef(cfg.Tracef, "admin http listen failed addr=%q error=%v", cfg.AdminWebAddr, err)
			return err
		}
	}

	tracef(cfg.Tracef, "daemon ssh listen begin addr=%q", cfg.SSHListenAddr)
	listener, err := net.Listen("tcp", cfg.SSHListenAddr)
	if err != nil {
		tracef(cfg.Tracef, "daemon ssh listen failed addr=%q error=%v", cfg.SSHListenAddr, err)
		return err
	}
	defer listener.Close()

	shutdown := newDaemonShutdownCoordinator(cfg.Tracef,
		daemonShutdownStep{name: "close ssh listener", run: func() error {
			return closeDaemonListener(listener)
		}},
		daemonShutdownStep{name: "close admin socket listener", run: func() error {
			return closeDaemonListener(adminSocketListener)
		}},
		daemonShutdownStep{name: "close admin http listener", run: func() error {
			return closeDaemonListener(adminHTTPListener)
		}},
		daemonShutdownStep{name: "stop active project runtimes", run: func() error {
			if container.ProjectRuntime == nil {
				return nil
			}
			return container.ProjectRuntime.StopAllProjects()
		}},
	)
	defer shutdown.Shutdown("run exit")
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		_ = shutdown.Shutdown("context canceled")
	}()

	var serveWG sync.WaitGroup
	serveWG.Add(1)
	go func() {
		defer serveWG.Done()
		tracef(cfg.Tracef, "admin socket serve begin")
		diagnostic.Background(service.Diagnostics(), "serve admin socket", admin.NewSocketServer(container.AdminService).Serve(ctx, adminSocketListener))
	}()
	if adminHTTPListener != nil {
		serveWG.Add(1)
		go func() {
			defer serveWG.Done()
			tracef(cfg.Tracef, "admin http serve begin addr=%q", adminHTTPListener.Addr().String())
			diagnostic.Background(service.Diagnostics(), "serve admin http", admin.NewHTTPServer(container.AdminService).ServeListener(ctx, adminHTTPListener))
		}()
	}

	hostSigner, err := LoadOrCreateSSHHostSigner(cfg.SSHHostKeyPath)
	if err != nil {
		tracef(cfg.Tracef, "daemon host key load failed path=%q error=%v", cfg.SSHHostKeyPath, err)
		return err
	}
	container.AdminService.SetListenAddr(listener.Addr().String())
	tracef(cfg.Tracef, "daemon ssh listening addr=%q host_key=%q", listener.Addr().String(), cfg.SSHHostKeyPath)
	if _, err := io.WriteString(stdout, listener.Addr().String()+"\n"); err != nil {
		tracef(cfg.Tracef, "daemon stdout write listen addr failed error=%v", err)
		return err
	}
	err = RunSSHListener(ctx, service, container.AdminStore, listener, hostSigner, cfg.Tracef)
	shutdownErr := shutdown.Shutdown("ssh listener stopped")
	serveWG.Wait()
	// Wait for the shutdown goroutine to finish so project runtimes (including
	// FUSE mounts) are fully stopped before the process exits.
	<-shutdownDone
	tracef(cfg.Tracef, "daemon ssh listener stopped error=%v shutdown_error=%v", err, shutdownErr)
	if err != nil && shutdownErr != nil {
		return errors.Join(err, shutdownErr)
	}
	if err != nil {
		return err
	}
	return shutdownErr
}

func withAdminDefaults(cfg config.DaemonConfig) config.DaemonConfig {
	if strings.TrimSpace(cfg.ProjectsRoot) == "" {
		return cfg
	}
	if strings.TrimSpace(cfg.AdminRoot) == "" {
		cfg.AdminRoot = filepath.Join(cfg.ProjectsRoot, "admin")
	}
	if strings.TrimSpace(cfg.AdminSocketPath) == "" {
		cfg.AdminSocketPath = filepath.Join(cfg.ProjectsRoot, "admin.sock")
	}
	if strings.TrimSpace(cfg.AdminWebAddr) == "" {
		cfg.AdminWebAddr = "127.0.0.1:0"
	}
	return cfg
}

func normalizeAdminHTTPServeError(service *admin.Service, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use") {
		if service != nil {
			service.SetAdminWebAddr("")
		}
		return nil
	}
	return err
}

func newDaemonShutdownCoordinator(trace traceFunc, steps ...daemonShutdownStep) *daemonShutdownCoordinator {
	return &daemonShutdownCoordinator{trace: trace, steps: steps}
}

func (c *daemonShutdownCoordinator) Shutdown(reason string) error {
	if c == nil {
		return nil
	}
	c.once.Do(func() {
		tracef(c.trace, "daemon shutdown begin reason=%q", reason)
		var errs []error
		for _, step := range c.steps {
			if step.run == nil {
				continue
			}
			err := normalizeDaemonShutdownError(step.run())
			if err != nil {
				tracef(c.trace, "daemon shutdown step failed name=%q error=%v", step.name, err)
				errs = append(errs, fmt.Errorf("%s: %w", step.name, err))
				continue
			}
			tracef(c.trace, "daemon shutdown step complete name=%q", step.name)
		}
		c.err = errors.Join(errs...)
		tracef(c.trace, "daemon shutdown end reason=%q error=%v", reason, c.err)
	})
	return c.err
}

func closeDaemonListener(listener net.Listener) error {
	if listener == nil {
		return nil
	}
	return listener.Close()
}

func normalizeDaemonShutdownError(err error) error {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
