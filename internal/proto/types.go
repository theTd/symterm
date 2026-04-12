package proto

import (
	"fmt"
	"strings"
	"time"
)

type Role string

const (
	RoleOwner    Role = "owner"
	RoleFollower Role = "follower"
)

type SessionKind string

const (
	SessionKindInteractive SessionKind = "interactive"
	SessionKindAuthority   SessionKind = "authority"
)

func NormalizeSessionKind(kind SessionKind) SessionKind {
	switch SessionKind(strings.TrimSpace(string(kind))) {
	case SessionKindAuthority:
		return SessionKindAuthority
	default:
		return SessionKindInteractive
	}
}

type AuthorityState string

const (
	AuthorityStateStable    AuthorityState = "stable"
	AuthorityStateRebinding AuthorityState = "rebinding"
	AuthorityStateAbsent    AuthorityState = "absent"
)

func NormalizeAuthorityState(state AuthorityState) AuthorityState {
	switch AuthorityState(strings.TrimSpace(string(state))) {
	case AuthorityStateRebinding:
		return AuthorityStateRebinding
	case AuthorityStateAbsent:
		return AuthorityStateAbsent
	default:
		return AuthorityStateStable
	}
}

func AuthorityReady(state AuthorityState) bool {
	return NormalizeAuthorityState(state) == AuthorityStateStable
}

type ProjectState string

const (
	ProjectStateInitializing      ProjectState = "initializing"
	ProjectStateSyncing           ProjectState = "syncing"
	ProjectStateActive            ProjectState = "active"
	ProjectStateNeedsConfirmation ProjectState = "needs-confirmation"
	ProjectStateTerminating       ProjectState = "terminating"
	ProjectStateTerminated        ProjectState = "terminated"
)

type SourceDiffLevel string

const (
	SourceDiffNone   SourceDiffLevel = "none"
	SourceDiffMinor  SourceDiffLevel = "minor"
	SourceDiffSevere SourceDiffLevel = "severe"
)

type WarningCode string

const (
	WarningSourceDrift WarningCode = "source-drift"
)

type ErrorCode string

const (
	ErrInvalidArgument       ErrorCode = "invalid-argument"
	ErrAuthenticationFailed  ErrorCode = "authentication-failed"
	ErrPermissionDenied      ErrorCode = "permission-denied"
	ErrOwnerMissing          ErrorCode = "owner-missing"
	ErrFollowerSyncDenied    ErrorCode = "follower-sync-denied"
	ErrNeedsConfirmation     ErrorCode = "needs-confirmation"
	ErrReadOnlyProject       ErrorCode = "read-only-project"
	ErrProjectNotReady       ErrorCode = "project-not-ready"
	ErrProjectInitFailed     ErrorCode = "project-init-failed"
	ErrProjectTerminated     ErrorCode = "project-terminated"
	ErrSyncEpochMismatch     ErrorCode = "sync-epoch-mismatch"
	ErrSyncRescanMismatch    ErrorCode = "sync-rescan-mismatch"
	ErrUnsupportedPath       ErrorCode = "unsupported-path"
	ErrFileCommitFailed      ErrorCode = "file-commit-failed"
	ErrOwnerWriteFailed      ErrorCode = "owner-write-failed"
	ErrCursorExpired         ErrorCode = "cursor-expired"
	ErrUnknownClient         ErrorCode = "unknown-client"
	ErrUnknownCommand        ErrorCode = "unknown-command"
	ErrUnknownFile           ErrorCode = "unknown-file"
	ErrUnknownHandle         ErrorCode = "unknown-handle"
	ErrReconcilePrecondition ErrorCode = "reconcile-precondition-failed"
	ErrConflict              ErrorCode = "conflict"
	ErrMountFailed           ErrorCode = "mount-failed"
	ErrTransportInterrupted  ErrorCode = "transport-interrupted"
)

type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func NewError(code ErrorCode, message string) *Error {
	return &Error{Code: code, Message: message}
}

type WorkspaceDigest struct {
	Algorithm string
	Value     string
}

func (d WorkspaceDigest) IsZero() bool {
	return d.Algorithm == "" && d.Value == ""
}

type ProjectKey struct {
	Username  string
	ProjectID string
}

func (k ProjectKey) String() string {
	return fmt.Sprintf("%s/%s", k.Username, k.ProjectID)
}

type Warning struct {
	Code      WarningCode
	Message   string
	DiffLevel SourceDiffLevel
}

type TTYSpec struct {
	Interactive bool
	Columns     int
	Rows        int
}

type CommandState string

const (
	CommandStateRunning CommandState = "running"
	CommandStateExited  CommandState = "exited"
	CommandStateFailed  CommandState = "failed"
)

type CommandSnapshot struct {
	CommandID     string
	ArgvTail      []string
	TTY           TTYSpec
	StartedBy     string
	StartedByRole Role
	TmuxStatus    bool
	State         CommandState
	StartedAt     time.Time
	ExitedAt      *time.Time
	ExitCode      *int
	FailureReason string
}

// Public control-plane snapshot returned by EnsureProject / ConfirmReconcile / FinalizeSync.
type ProjectSnapshot struct {
	ProjectID                string
	Role                     Role
	AuthorityState           AuthorityState
	OwnerWorkspaceInstanceID string
	ProjectState             ProjectState
	CommandSnapshots         []CommandSnapshot
	CanStartCommands         bool
	SyncEpoch                uint64
	NeedsConfirmation        bool
	Warnings                 []Warning
	CurrentCursor            uint64
}

// Public control-plane request types. These are the design-facing RPC contracts.
type HelloRequest struct {
	Version             string
	ProjectID           string
	TransportKind       string
	LocalWorkspaceRoot  string
	WorkspaceInstanceID string
	SessionKind         SessionKind
	Capabilities        []string
	WorkspaceDigest     WorkspaceDigest
}

type EnsureProjectRequest struct {
	ProjectID string
}

type OpenProjectSessionRequest struct {
	ProjectID string
}

type ProjectSessionResponse struct {
	Snapshot ProjectSnapshot
}

type SyncCapabilities struct {
	ProtocolVersion     uint32 `json:"protocol_version"`
	ManifestBatch       bool   `json:"manifest_batch"`
	DeleteBatch         bool   `json:"delete_batch"`
	UploadBundle        bool   `json:"upload_bundle"`
	PersistentHashCache bool   `json:"persistent_hash_cache"`
}

type ConfirmReconcileRequest struct {
	ProjectID       string
	ExpectedCursor  uint64
	WorkspaceDigest WorkspaceDigest
}

type ResumeProjectSessionRequest struct {
	ProjectID       string
	ExpectedCursor  uint64
	WorkspaceDigest WorkspaceDigest
}

type WatchProjectRequest struct {
	ProjectID   string
	SinceCursor uint64
}

// Fine-grained initial-sync RPC payloads kept as compatibility/internal steps.
// Default client session control should prefer open/resume/start project-session RPCs.
type FileMetadata struct {
	Mode  uint32
	MTime time.Time
	Size  int64
}

type ManifestEntry struct {
	Path            string
	IsDir           bool
	Metadata        FileMetadata
	StatFingerprint string
	ContentHash     string
}

type BeginSyncRequest struct {
	SyncEpoch       uint64
	AttemptID       uint64
	RootFingerprint string
}

type StartSyncSessionRequest struct {
	SyncEpoch       uint64
	AttemptID       uint64
	RootFingerprint string
}

type StartSyncSessionResponse struct {
	SessionID             string
	SyncEpoch             uint64
	ProtocolVersion       uint32
	RemoteGeneration      uint64
	RemoteManifestEntries uint64
}

type ScanManifestRequest struct {
	Entries []ManifestEntry
}

type SyncManifestBatchRequest struct {
	SessionID string
	Entries   []ManifestEntry
	Final     bool
}

type PlanManifestHashesResponse struct {
	Paths []string
}

type PlanSyncActionsResponse struct {
	UploadPaths []string
	DeletePaths []string
}

type PlanSyncV2Request struct {
	SessionID string
}

type PlanSyncV2Response struct {
	HashPaths   []string
	UploadPaths []string
	DeletePaths []string
}

type FsMutationRequest struct {
	Op            FsOperation
	Request       FsRequest
	Preconditions []MutationPrecondition
}

type ResizeTTYResponse struct {
	Applied bool
}

type BeginFileRequest struct {
	SyncEpoch       uint64
	Path            string
	Metadata        FileMetadata
	ExpectedSize    int64
	StatFingerprint string
}

type BeginFileResponse struct {
	FileID string
}

type ApplyChunkRequest struct {
	FileID   string
	Offset   int64
	Data     []byte
	Checksum string
}

type CommitFileRequest struct {
	FileID          string
	FinalHash       string
	FinalSize       int64
	MTime           time.Time
	Mode            uint32
	StatFingerprint string
}

type AbortFileRequest struct {
	FileID string
	Reason string
}

type MutationPrecondition struct {
	Path             string
	ObjectGeneration uint64
	DirGeneration    uint64
	MustNotExist     bool
	ObjectIdentity   string
}

type DeletePathRequest struct {
	SyncEpoch    uint64
	Path         string
	Precondition MutationPrecondition
}

type DeletePathsBatchRequest struct {
	SessionID string
	SyncEpoch uint64
	Paths     []string
}

type UploadBundleBeginRequest struct {
	SessionID string
	SyncEpoch uint64
}

type UploadBundleBeginResponse struct {
	BundleID string
}

type UploadBundleFile struct {
	Path            string
	Metadata        FileMetadata
	StatFingerprint string
	ContentHash     string
	Data            []byte
}

type UploadBundleCommitRequest struct {
	SessionID string
	BundleID  string
	Files     []UploadBundleFile
}

type FinalizeSyncRequest struct {
	SyncEpoch   uint64
	AttemptID   uint64
	GuardStable bool
}

type FinalizeSyncV2Request struct {
	SessionID   string
	SyncEpoch   uint64
	AttemptID   uint64
	GuardStable bool
}

// Internal sync-progress reporting RPC used by the active owner during initial convergence.
type ReportSyncProgressRequest struct {
	SyncEpoch uint64
	Progress  SyncProgress
}

// Public shared-workspace filesystem operations exposed through the control plane.
type FsOperation string

const (
	FsOpLookup   FsOperation = "lookup"
	FsOpGetAttr  FsOperation = "getattr"
	FsOpReadDir  FsOperation = "readdir"
	FsOpOpen     FsOperation = "open"
	FsOpRead     FsOperation = "read"
	FsOpWrite    FsOperation = "write"
	FsOpCreate   FsOperation = "create"
	FsOpMkdir    FsOperation = "mkdir"
	FsOpRemove   FsOperation = "remove"
	FsOpRmdir    FsOperation = "rmdir"
	FsOpRename   FsOperation = "rename"
	FsOpTruncate FsOperation = "truncate"
	FsOpChmod    FsOperation = "chmod"
	FsOpUtimens  FsOperation = "utimens"
	FsOpFSync    FsOperation = "fsync"
	FsOpFlush    FsOperation = "flush"
	FsOpRelease  FsOperation = "release"
)

type FsOpenIntent string

const (
	FsOpenIntentUnspecified FsOpenIntent = ""
	FsOpenIntentReadOnly    FsOpenIntent = "readonly"
	FsOpenIntentWritable    FsOpenIntent = "writable"
)

type FsRequest struct {
	Path       string
	NewPath    string
	HandleID   string
	OpenIntent FsOpenIntent
	Offset     int64
	Size       int64
	Mode       uint32
	Data       []byte
}

type FsReply struct {
	ObjectGeneration uint64
	DirGeneration    uint64
	Conflict         bool
	ReadOnly         bool
	HandleID         string
	Invalidations    []InvalidateChange
	Metadata         FileMetadata
	IsDir            bool
	Data             []byte
}

type OwnerFileApplyRequest struct {
	Op       FsOperation
	Path     string
	NewPath  string
	Metadata FileMetadata
	Data     []byte
}

type OwnerFileBeginRequest struct {
	Op           FsOperation
	Path         string
	Metadata     FileMetadata
	ExpectedSize int64
}

type OwnerFileBeginResponse struct {
	UploadID string
}

type OwnerFileApplyChunkRequest struct {
	UploadID string
	Offset   int64
	Data     []byte
}

type OwnerFileCommitRequest struct {
	UploadID string
}

type OwnerFileAbortRequest struct {
	UploadID string
	Reason   string
}

// Internal owner-authority bridge RPCs. These are not part of the public control-plane contract.
type WatchInvalidateRequest struct {
	ProjectID   string
	SinceCursor uint64
}

type InvalidateRequest struct {
	Changes []InvalidateChange
}

type OwnerWatcherFailureRequest struct {
	Reason string
}

type InvalidateEvent struct {
	Cursor    uint64
	Timestamp time.Time
	Changes   []InvalidateChange
}

type InvalidateKind string

const (
	InvalidateData   InvalidateKind = "data"
	InvalidateDentry InvalidateKind = "dentry"
	InvalidateDelete InvalidateKind = "delete"
	InvalidateRename InvalidateKind = "rename"
)

type InvalidateChange struct {
	Path    string
	NewPath string
	Kind    InvalidateKind
}

type StartCommandRequest struct {
	ProjectID  string
	ArgvTail   []string
	TTY        TTYSpec
	TmuxStatus bool
}

type StartProjectCommandSessionRequest struct {
	ProjectID  string
	ArgvTail   []string
	TTY        TTYSpec
	TmuxStatus bool
}

type TmuxStatusSnapshot struct {
	ClientID         string
	ProjectID        string
	Role             Role
	CommandID        string
	CommandState     CommandState
	ProjectState     ProjectState
	ControlConnected bool
	StdioConnected   bool
	StdioBytesIn     uint64
	StdioBytesOut    uint64
	LastActivityAt   time.Time
	AttachedCommands int
}

// Transport/result helper types used by the JSON-RPC layer.
type StartCommandResponse struct {
	CommandID string
}

type StartProjectCommandSessionResponse struct {
	Snapshot ProjectSnapshot
	Command  *StartCommandResponse
}

type AttachStdioRequest struct {
	CommandID    string
	StdoutOffset int64
	StderrOffset int64
}

type AttachStdioResponse struct {
	Stdout       []byte
	Stderr       []byte
	StdoutOffset int64
	StderrOffset int64
	Complete     bool
}

type ResizeTTYRequest struct {
	CommandID string
	Columns   int
	Rows      int
}

type SendSignalRequest struct {
	CommandID string
	Name      string
}

type SendSignalResponse struct {
	Delivered bool
}

type WatchCommandRequest struct {
	CommandID   string
	SinceCursor uint64
}

type ProjectEventType string

const (
	ProjectEventOwnerChanged          ProjectEventType = "owner-changed"
	ProjectEventNeedsConfirmation     ProjectEventType = "needs-confirmation-changed"
	ProjectEventSyncStateChanged      ProjectEventType = "sync-state-changed"
	ProjectEventSyncProgress          ProjectEventType = "sync-progress"
	ProjectEventCommandStarted        ProjectEventType = "command-started"
	ProjectEventCommandUpdated        ProjectEventType = "command-updated"
	ProjectEventAuthorityStateChanged ProjectEventType = "authority-state-changed"
	ProjectEventInstanceTerminated    ProjectEventType = "instance-terminated"
)

type SyncProgressPhase string

const (
	SyncProgressPhaseBegin        SyncProgressPhase = "begin"
	SyncProgressPhaseScanManifest SyncProgressPhase = "scan-manifest"
	SyncProgressPhaseHashManifest SyncProgressPhase = "hash-manifest"
	SyncProgressPhaseUploadFiles  SyncProgressPhase = "upload-files"
	SyncProgressPhaseFinalize     SyncProgressPhase = "finalize"
)

type SyncProgress struct {
	Phase     SyncProgressPhase
	Completed uint64
	Total     uint64
	Percent   *int
}

type ProjectEvent struct {
	Cursor                   uint64
	Type                     ProjectEventType
	Timestamp                time.Time
	OwnerID                  string
	AuthorityState           AuthorityState
	OwnerWorkspaceInstanceID string
	ProjectState             ProjectState
	NeedsConfirmation        bool
	SyncEpoch                uint64
	Warning                  *Warning
	SyncProgress             *SyncProgress
	Command                  *CommandSnapshot
}

type CommandEventType string

const (
	CommandEventExecStarted CommandEventType = "exec-started"
	CommandEventAttachStdio CommandEventType = "stdio-attached"
	CommandEventDetachStdio CommandEventType = "stdio-detached"
	CommandEventTTYResized  CommandEventType = "tty-resized"
	CommandEventSignalSent  CommandEventType = "signal-sent"
	CommandEventExited      CommandEventType = "exited"
	CommandEventExecFailed  CommandEventType = "exec-failed"
	CommandEventIOClosed    CommandEventType = "io-closed"
)

type CommandEvent struct {
	Cursor    uint64
	CommandID string
	Type      CommandEventType
	Timestamp time.Time
	ExitCode  *int
	Message   string
}
