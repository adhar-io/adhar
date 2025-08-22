package types

import (
	"time"
)

// TypeMeta describes an individual object in an API response or request
type TypeMeta struct {
	Kind       string `json:"kind,omitempty"`
	APIVersion string `json:"apiVersion,omitempty"`
}

// ObjectMeta is metadata that all persisted resources must have
type ObjectMeta struct {
	Name   string            `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// ClusterSpec defines the desired state of a Kubernetes cluster
type ClusterSpec struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata,omitempty"`

	Provider     string            `json:"provider"`
	Region       string            `json:"region"`
	Version      string            `json:"version"`
	ControlPlane ControlPlaneSpec  `json:"controlPlane"`
	NodeGroups   []NodeGroupSpec   `json:"nodeGroups"`
	Networking   NetworkingSpec    `json:"networking"`
	Security     SecuritySpec      `json:"security"`
	Addons       AddonsSpec        `json:"addons"`
	Domain       *DomainConfig     `json:"domain,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// Cluster represents a running Kubernetes cluster
type Cluster struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Provider  string                 `json:"provider"`
	Region    string                 `json:"region"`
	Version   string                 `json:"version"`
	Status    ClusterStatus          `json:"status"`
	Endpoint  string                 `json:"endpoint,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
	Tags      map[string]string      `json:"tags,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ClusterStatus represents the current status of a cluster
type ClusterStatus string

const (
	ClusterStatusCreating ClusterStatus = "creating"
	ClusterStatusRunning  ClusterStatus = "running"
	ClusterStatusUpdating ClusterStatus = "updating"
	ClusterStatusDeleting ClusterStatus = "deleting"
	ClusterStatusError    ClusterStatus = "error"
	ClusterStatusUnknown  ClusterStatus = "unknown"
)

// ControlPlaneSpec defines the control plane configuration
type ControlPlaneSpec struct {
	Replicas         int            `json:"replicas"`
	InstanceType     string         `json:"instanceType"`
	HighAvailability bool           `json:"highAvailability"`
	ETCD             ETCDSpec       `json:"etcd"`
	APIServer        APIServerSpec  `json:"apiServer"`
	Encryption       EncryptionSpec `json:"encryption"`
}

// ETCDSpec defines etcd configuration
type ETCDSpec struct {
	External       bool   `json:"external"`
	Replicas       int    `json:"replicas"`
	BackupSchedule string `json:"backupSchedule,omitempty"`
}

// APIServerSpec defines API server configuration
type APIServerSpec struct {
	ExtraArgs map[string]string `json:"extraArgs,omitempty"`
	CertSANs  []string          `json:"certSANs,omitempty"`
}

// NodeGroupSpec defines node group configuration
type NodeGroupSpec struct {
	Name         string            `json:"name"`
	Replicas     int               `json:"replicas"`
	InstanceType string            `json:"instanceType"`
	AutoScaling  AutoScalingSpec   `json:"autoScaling,omitempty"`
	Taints       []TaintSpec       `json:"taints,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	UserData     string            `json:"userData,omitempty"`
}

// NodeGroup represents a running node group
type NodeGroup struct {
	Name         string            `json:"name"`
	Replicas     int               `json:"replicas"`
	InstanceType string            `json:"instanceType"`
	Status       NodeGroupStatus   `json:"status"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
	Labels       map[string]string `json:"labels,omitempty"`
}

// NodeGroupStatus represents the current status of a node group
type NodeGroupStatus string

const (
	NodeGroupStatusCreating NodeGroupStatus = "creating"
	NodeGroupStatusReady    NodeGroupStatus = "ready"
	NodeGroupStatusScaling  NodeGroupStatus = "scaling"
	NodeGroupStatusDeleting NodeGroupStatus = "deleting"
	NodeGroupStatusError    NodeGroupStatus = "error"
)

// AutoScalingSpec defines auto scaling configuration
type AutoScalingSpec struct {
	MinReplicas int `json:"minReplicas"`
	MaxReplicas int `json:"maxReplicas"`
}

// TaintSpec defines node taints
type TaintSpec struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
}

// NetworkingSpec defines networking configuration
type NetworkingSpec struct {
	CNI         string `json:"cni"`
	PodCIDR     string `json:"podCIDR"`
	ServiceCIDR string `json:"serviceCIDR"`
	ClusterDNS  string `json:"clusterDNS"`
	IPv6        bool   `json:"ipv6,omitempty"`
	HTTPPort    int    `json:"httpPort,omitempty"`  // Host port for HTTP traffic (default: 80)
	HTTPSPort   int    `json:"httpsPort,omitempty"` // Host port for HTTPS traffic (default: 443)
}

// SecuritySpec defines security configuration
type SecuritySpec struct {
	RBAC                 bool              `json:"rbac"`
	NetworkPolicies      bool              `json:"networkPolicies"`
	PodSecurityStandards string            `json:"podSecurityStandards"`
	Encryption           EncryptionSpec    `json:"encryption"`
	ImageSecurity        ImageSecuritySpec `json:"imageSecurity"`
	AuditLogging         AuditLoggingSpec  `json:"auditLogging"`
}

// EncryptionSpec defines encryption configuration
type EncryptionSpec struct {
	ETCD    bool `json:"etcd"`
	Secrets bool `json:"secrets"`
}

// ImageSecuritySpec defines image security configuration
type ImageSecuritySpec struct {
	ScanImages     bool     `json:"scanImages"`
	AllowedSources []string `json:"allowedSources,omitempty"`
	BlockedSources []string `json:"blockedSources,omitempty"`
}

// AuditLoggingSpec defines audit logging configuration
type AuditLoggingSpec struct {
	Enabled bool   `json:"enabled"`
	Backend string `json:"backend,omitempty"`
}

// AddonsSpec defines addon configuration
type AddonsSpec struct {
	Monitoring MonitoringSpec `json:"monitoring"`
	Logging    LoggingSpec    `json:"logging"`
	Ingress    IngressSpec    `json:"ingress"`
	Backup     BackupSpec     `json:"backup"`
	Security   SecurityAddons `json:"security"`
}

// MonitoringSpec defines monitoring addon configuration
type MonitoringSpec struct {
	Prometheus   bool `json:"prometheus"`
	Grafana      bool `json:"grafana"`
	Alertmanager bool `json:"alertmanager"`
}

// LoggingSpec defines logging addon configuration
type LoggingSpec struct {
	Fluentd       bool `json:"fluentd"`
	Elasticsearch bool `json:"elasticsearch"`
	Kibana        bool `json:"kibana"`
}

// IngressSpec defines ingress addon configuration
type IngressSpec struct {
	NGINX       bool `json:"nginx"`
	CertManager bool `json:"certManager"`
}

// BackupSpec defines backup addon configuration
type BackupSpec struct {
	Velero   bool   `json:"velero"`
	Schedule string `json:"schedule,omitempty"`
}

// SecurityAddons defines security addon configuration
type SecurityAddons struct {
	Falco bool `json:"falco"`
	OPA   bool `json:"opa"`
}

// DomainConfig defines domain configuration for the cluster
type DomainConfig struct {
	Name            string            `json:"name"`
	BaseDomain      string            `json:"baseDomain"`                // Base domain for the cluster
	CertificateType string            `json:"certificateType,omitempty"` // letsencrypt, selfsigned, custom
	DNSProvider     string            `json:"dnsProvider,omitempty"`     // cloudflare, route53, etc.
	DNSConfig       map[string]string `json:"dnsConfig,omitempty"`       // provider-specific DNS configuration
	TLS             TLSConfig         `json:"tls,omitempty"`
	DNS             DNSConfig         `json:"dns,omitempty"`
	Ingress         IngressConfig     `json:"ingress,omitempty"`
}

// TLSConfig defines TLS certificate configuration
type TLSConfig struct {
	Enabled     bool   `json:"enabled"`
	Email       string `json:"email,omitempty"`
	Environment string `json:"environment,omitempty"` // staging, production
}

// DNSConfig defines DNS provider configuration
type DNSConfig struct {
	Provider string            `json:"provider,omitempty"`
	Config   map[string]string `json:"config,omitempty"`
}

// IngressConfig defines ingress controller configuration
type IngressConfig struct {
	Provider string            `json:"provider,omitempty"`
	Config   map[string]string `json:"config,omitempty"`
}

// VPCSpec defines VPC configuration
type VPCSpec struct {
	CIDR              string            `json:"cidr"`
	AvailabilityZones []string          `json:"availabilityZones"`
	Tags              map[string]string `json:"tags,omitempty"`
}

// VPC represents a virtual private cloud
type VPC struct {
	ID                string            `json:"id"`
	CIDR              string            `json:"cidr"`
	AvailabilityZones []string          `json:"availabilityZones"`
	Status            string            `json:"status"`
	Tags              map[string]string `json:"tags,omitempty"`
}

// LoadBalancerSpec defines load balancer configuration
type LoadBalancerSpec struct {
	Type  string            `json:"type"`
	Ports []PortSpec        `json:"ports"`
	Tags  map[string]string `json:"tags,omitempty"`
}

// LoadBalancer represents a load balancer
type LoadBalancer struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Endpoint string            `json:"endpoint"`
	Status   string            `json:"status"`
	Tags     map[string]string `json:"tags,omitempty"`
}

// PortSpec defines port configuration
type PortSpec struct {
	Port       int    `json:"port"`
	TargetPort int    `json:"targetPort"`
	Protocol   string `json:"protocol"`
}

// StorageSpec defines storage configuration
type StorageSpec struct {
	Type string            `json:"type"`
	Size string            `json:"size"`
	Tags map[string]string `json:"tags,omitempty"`
}

// Storage represents a storage volume
type Storage struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Size   string            `json:"size"`
	Status string            `json:"status"`
	Tags   map[string]string `json:"tags,omitempty"`
}

// Backup represents a cluster backup
type Backup struct {
	ID        string    `json:"id"`
	ClusterID string    `json:"clusterId"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	Size      string    `json:"size"`
}

// HealthStatus represents cluster health information
type HealthStatus struct {
	Status     string                     `json:"status"`
	Components map[string]ComponentHealth `json:"components"`
	LastCheck  time.Time                  `json:"lastCheck"`
}

// ComponentHealth represents individual component health
type ComponentHealth struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// Metrics represents cluster metrics
type Metrics struct {
	CPU     MetricValue `json:"cpu"`
	Memory  MetricValue `json:"memory"`
	Disk    MetricValue `json:"disk"`
	Network MetricValue `json:"network"`
}

// MetricValue represents a metric value with usage and capacity
type MetricValue struct {
	Usage    string  `json:"usage"`
	Capacity string  `json:"capacity"`
	Percent  float64 `json:"percent"`
}

// Credentials represents provider credentials
type Credentials struct {
	Type      string                 `json:"type"`
	Data      map[string]interface{} `json:"data"`
	SecretRef *SecretReference       `json:"secretRef,omitempty"`
}

// SecretReference references a Kubernetes secret
type SecretReference struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

// ClusterCost represents cluster cost information
type ClusterCost struct {
	TotalCost float64            `json:"totalCost"`
	Currency  string             `json:"currency"`
	Breakdown map[string]float64 `json:"breakdown"`
	Period    string             `json:"period"` // monthly, hourly, daily
}

// ProviderHealth represents provider health status
type ProviderHealth struct {
	Status      string                     `json:"status"`
	Provider    string                     `json:"provider"`
	Region      string                     `json:"region"`
	LastCheck   time.Time                  `json:"lastCheck"`
	Components  map[string]ComponentHealth `json:"components"`
	Connections map[string]bool            `json:"connections"`
}

// ClusterAddon represents a cluster addon
type ClusterAddon struct {
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Status      string                 `json:"status"`
	Description string                 `json:"description,omitempty"`
	Config      map[string]interface{} `json:"config,omitempty"`
	InstalledAt time.Time              `json:"installedAt"`
	UpdatedAt   time.Time              `json:"updatedAt"`
}

// AddonSpec represents addon installation specification
type AddonSpec struct {
	Name         string                 `json:"name"`
	Version      string                 `json:"version,omitempty"`
	Enabled      bool                   `json:"enabled"`
	Config       map[string]interface{} `json:"config,omitempty"`
	Dependencies []string               `json:"dependencies,omitempty"`
}
