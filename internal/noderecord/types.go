package noderecord

import (
	"encoding/json"
	"time"

	medik8sv1alpha1 "github.com/medik8s/storage-based-remediation/v5/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

const AnnotationKey = "sbr.medik8s.io/remediation-owner"

const SchemaVersion = 1

type ExecutionPhase string

const (
	PhaseOffered    ExecutionPhase = "Offered"
	PhaseAccepted   ExecutionPhase = "Accepted"
	PhaseCancelled  ExecutionPhase = "Cancelled"
	PhaseSuperseded ExecutionPhase = "Superseded"
	PhaseBootEnded  ExecutionPhase = "BootEnded"
)

type OOSTaintPolicy string

const (
	PolicyOnTimeout OOSTaintPolicy = "OnTimeout"
	PolicyOnReboot  OOSTaintPolicy = "OnReboot"
)

type Record struct {
	SchemaVersion        int                        `json:"schemaVersion"`
	NodeUID              types.UID                  `json:"nodeUID"`
	Execution            *Execution                 `json:"execution,omitempty"`
	PreviousExecutions   []Execution                `json:"previousExecutions,omitempty"`
	Policy               *Policy                    `json:"policy,omitempty"`
	Budget               Budget                     `json:"budget"`
	Restrictions         Restrictions               `json:"restrictions"`
	CoordinationRevision int64                      `json:"coordinationRevision"`
	Unknown              map[string]json.RawMessage `json:"-"`
}

type CRRef struct {
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	UID       types.UID `json:"uid"`
}

type ConfigRef struct {
	UID        types.UID                                   `json:"uid"`
	VolumeMode medik8sv1alpha1.SharedStorageVolumeModeType `json:"volumeMode"`
}

type Execution struct {
	CR             CRRef          `json:"cr"`
	Config         ConfigRef      `json:"config"`
	InitialBootID  string         `json:"initialBootID"`
	Phase          ExecutionPhase `json:"phase"`
	ObservedBootID string         `json:"observedBootID,omitempty"`
}

type Policy struct {
	RequestUID      types.UID      `json:"requestUID"`
	TaintPolicy     OOSTaintPolicy `json:"taintPolicy"`
	OfferedAt       time.Time      `json:"offeredAt"`
	RebootDeadline  time.Time      `json:"rebootDeadline"`
	TaintConfirmed  bool           `json:"taintConfirmed"`
	CanStartOrRetry bool           `json:"canStartOrRetry"`
}

type Budget struct {
	HistoryKnown bool     `json:"historyKnown"`
	OutageIDs    []string `json:"outageIDs,omitempty"`
}

type Restrictions struct {
	OriginalCordon   *bool       `json:"originalCordon,omitempty"`
	OriginalOOSTaint *bool       `json:"originalOOSTaint,omitempty"`
	RequiredByCRs    []types.UID `json:"requiredByCRs,omitempty"`
}

type recordAlias Record

var knownKeys = []string{
	"schemaVersion", "nodeUID", "execution", "previousExecutions",
	"policy", "budget", "restrictions", "coordinationRevision",
}

const MaxRecordBytes = 64 * 1024
