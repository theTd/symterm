package setup

import (
	"context"
	"time"

	"symterm/internal/admin"
	"symterm/internal/buildinfo"
	"symterm/internal/config"
	"symterm/internal/control"
)

type DaemonContainer struct {
	Service        *control.Service
	ClientService  control.ClientService
	AdminSessions  control.AdminSessionService
	RuntimeControl control.ProjectRuntimeControl
	AdminStore     *admin.Store
	AdminService   *admin.Service
	ProjectRuntime *ProjectRuntimeFacade
}

func NewDaemonContainer(ctx context.Context, cfg config.DaemonConfig) (*DaemonContainer, error) {
	adminStore, err := admin.OpenStore(cfg.AdminRoot)
	if err != nil {
		return nil, err
	}
	eventHub, err := admin.NewEventHub(0)
	if err != nil {
		return nil, err
	}
	var service *control.Service
	runtimeFacade := NewProjectRuntimeFacade(
		ctx,
		cfg.ProjectsRoot,
		cfg.AllowUnsafeNoFuse,
		cfg.RemoteEntrypoint,
		adminStore.EffectiveEntrypoint,
		func() projectRuntimeService { return service },
		cfg.Tracef,
	)
	runtimeFacade.RuntimeManager().SetAdminSocketPath(cfg.AdminSocketPath)
	sessionObserver := admin.NewControlSessionObserver(eventHub)

	service, err = control.NewServiceWithDependencies(
		adminStore,
		control.ServiceDependencies{
			Runtime:  runtimeFacade,
			Sessions: sessionObserver,
			Tracef:   cfg.Tracef,
		},
	)
	if err != nil {
		return nil, err
	}
	adminService, err := admin.NewService(admin.NewControlSessionCatalog(service), eventHub, adminStore, admin.DaemonInfo{
		Version:         buildinfo.ResolveVersion(),
		StartedAt:       timeNowUTC(),
		AdminSocketPath: cfg.AdminSocketPath,
		AdminWebAddr:    cfg.AdminWebAddr,
	}, timeNowUTC)
	if err != nil {
		return nil, err
	}
	adminService.SetTmuxStatusSource(service)

	return &DaemonContainer{
		Service:        service,
		ClientService:  service,
		AdminSessions:  service,
		RuntimeControl: service,
		AdminStore:     adminStore,
		AdminService:   adminService,
		ProjectRuntime: runtimeFacade,
	}, nil
}

func timeNowUTC() time.Time {
	return time.Now().UTC()
}
