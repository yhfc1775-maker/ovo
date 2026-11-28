package model

// NodeKernel is a proxy kernel (Xray, sing-box, ...) deployed on a node.
type NodeKernel struct {
	ID               int64  `gorm:"primaryKey;autoIncrement"`
	NodeID           int64  `gorm:"column:node_id;not null;uniqueIndex:idx_node_kernel_node_type"`
	KernelType       string `gorm:"column:kernel_type;type:varchar(20);not null;uniqueIndex:idx_node_kernel_node_type"`
	Version          string `gorm:"type:varchar(50);not null;default:'latest'"`
	InstalledVersion string `gorm:"column:installed_version;type:varchar(100);not null;default:''"`
	Status           string `gorm:"type:varchar(20);not null;default:'pending'"`
	LastError        string `gorm:"column:last_error;type:text;not null;default:''"`
	ConfigHash       string `gorm:"column:config_hash;type:varchar(64);not null;default:''"`
	AppliedHash      string `gorm:"column:applied_hash;type:varchar(64);not null;default:''"`
	Enabled          int    `gorm:"not null;default:1"`
	CreatedTime      int64  `gorm:"column:created_time;not null"`
	UpdatedTime      int64  `gorm:"column:updated_time;not null"`
}

func (NodeKernel) TableName() string { return "node_kernel" }

// NodeInbound is one listening proxy service served by a NodeKernel. The
// protocol specific parts are stored as JSON text (see internal/kernel).
type NodeInbound struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	KernelID  int64  `gorm:"column:kernel_id;not null;index"`
	NodeID    int64  `gorm:"column:node_id;not null;index"`
	Name      string `gorm:"type:varchar(100);not null"`
	Protocol  string `gorm:"type:varchar(30);not null"`
	Listen    string `gorm:"type:varchar(100);not null;default:'::'"`
	Port      int    `gorm:"not null"`
	Transport string `gorm:"type:text;not null;default:''"`
	Security  string `gorm:"type:text;not null;default:''"`
	Settings  string `gorm:"type:text;not null;default:''"`
	Extra     string `gorm:"type:text;not null;default:''"`
	// ShareAddressMode picks where the share/subscription address comes
	// from: auto (node default) / ipv4 / ipv6 / custom (ShareHost).
	ShareAddressMode string `gorm:"column:share_address_mode;type:varchar(16);not null;default:'auto'"`
	ShareHost        string `gorm:"column:share_host;type:varchar(255);not null;default:''"`
	// MuxPort is the internal loopback port (127.0.0.1:<MuxPort>) automatically
	// assigned when this inbound is served behind the node's NodePortMux (0 = not muxed).
	// While muxed the Xray render accepts the mux's PROXY protocol header
	// (AcceptProxyProtocol is set to 1 alongside; see handler/kernel_mux.go).
	MuxPort   int    `gorm:"column:mux_port;not null;default:0"`
	Remark    string `gorm:"type:text;not null;default:''"`
	RelayOnly int    `gorm:"column:relay_only;not null;default:0"`
	// AcceptProxyProtocol: relays send PROXY protocol so the kernel sees
	// the real client IP; direct access is then impossible (forces RelayOnly).
	AcceptProxyProtocol int `gorm:"column:accept_proxy_protocol;not null;default:0"`
	// RelayWhitelist drops traffic to the port unless it comes from a relay
	// node, loopback, or an entry of WhitelistExtra.
	RelayWhitelist int    `gorm:"column:relay_whitelist;not null;default:0"`
	WhitelistExtra string `gorm:"column:whitelist_extra;type:text;not null;default:''"`
	Enabled        int    `gorm:"not null;default:1"`
	UploadBytes    int64  `gorm:"column:upload_bytes;not null;default:0"`
	DownloadBytes  int64  `gorm:"column:download_bytes;not null;default:0"`
	Inx            int    `gorm:"not null;default:0"`
	CreatedTime    int64  `gorm:"column:created_time;not null"`
	UpdatedTime    int64  `gorm:"column:updated_time;not null"`
	// SourceID is the remote subscription an imported node came from (0 =
	// imported by hand); the source's refresh keeps it up to date.
	SourceID int64 `gorm:"column:source_id;not null;default:0;index"`
	// OutboundID chains the inbound's traffic through another inbound (one
	// of the panel's or an imported node) instead of leaving directly; 0 is
	// direct.
	OutboundID int64 `gorm:"column:outbound_id;not null;default:0;index"`
	// SplitRules are the 分流 rules (JSON list of kernel.SplitRule).
	SplitRules string `gorm:"column:split_rules;type:text;not null;default:''"`
	// OutboundIPStrategy scopes the inbound's outbound connections to an
	// IP family ("auto" = unrestricted; see kernel egress).
	OutboundIPStrategy string `gorm:"column:outbound_ip_strategy;type:varchar(20);not null;default:'auto'"`
	// ConnIdleSeconds is the per-inbound idle timeout (seconds) for Xray
	// (policy.levels[].connIdle). 0 = unset: no policy segment is
	// emitted and Xray's level-0 default (300s) applies; -1 = unlimited
	// (renders as 31536000 = 365d); 1..86400 are the other accepted
	// values (see kernel.Default/MaxConnIdleSeconds).
	// Non-Xray kernels refuse a non-zero value at save time.
	ConnIdleSeconds int `gorm:"column:conn_idle_seconds;not null;default:0"`
	// CompanionOf marks an SS metering companion inbound: a
	// system-managed row derived from the origin inbound it points to
	// (0 = normal inbound). The companion serves the origin's share
	// subscriptions with per-user SS2022 credentials so their traffic
	// can be counted per subscription; it is hidden from inbound lists,
	// subscription sources and backups, and is materialized/recycled by
	// reconcile (handler/subscription_metering.go). The partial unique
	// index keeps at most one companion per origin (normal rows all
	// carry 0 and are not covered by it).
	CompanionOf int64 `gorm:"column:companion_of;not null;default:0;index;uniqueIndex:idx_node_inbound_companion,where:companion_of > 0"`
	// MeteringEnabled is an origin inbound's subscription-metering
	// switch (self-built Shadowsocks 2022 inbounds only). While on, a
	// companion inbound is materialized whenever an active share
	// subscription sources this inbound.
	MeteringEnabled int `gorm:"column:metering_enabled;not null;default:0"`
	// SSSubscriptionUsers is the in-place SS subscription-metering
	// switch (the kernel render layer calls it InplaceUsers). While on, the
	// origin inbound itself carries its active share subscriptions
	// as multi-user clients and no metering companion is ever
	// materialized for it. MeteringEnabled is the legacy
	// companion-era switch and is no longer written by any save path
	// (its stored value is kept only so the metering status can point
	// legacy-metered origins at the new switch).
	SSSubscriptionUsers int `gorm:"column:ss_subscription_users;not null;default:0"`
}

func (NodeInbound) TableName() string { return "node_inbound" }

// NodePortMux is one node's TCP SNI multiplexer (443 port reuse): a single
// public port whose TLS ClientHello SNI decides which inbound (or external
// target) a connection goes to. The agent runs the splitter (see
// go-gost/x/kernel/sni_mux.go); the panel builds its route table from the
// node's muxed inbounds plus the admin's external rules.
type NodePortMux struct {
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	NodeID         int64  `gorm:"column:node_id;not null;uniqueIndex"`
	Enabled        int    `gorm:"not null;default:0"`
	Listen         string `gorm:"type:varchar(100);not null;default:'::'"`
	Port           int    `gorm:"not null;default:443"`
	Rules          string `gorm:"type:text;not null;default:''"`
	FallbackTarget string `gorm:"column:fallback_target;type:varchar(255);not null;default:''"`
	FallbackProxy  int    `gorm:"column:fallback_proxy;not null;default:0"`
	Status         string `gorm:"type:varchar(20);not null;default:'stopped'"`
	LastError      string `gorm:"column:last_error;type:text;not null;default:''"`
	ConfigHash     string `gorm:"column:config_hash;type:varchar(64);not null;default:''"`
	AppliedHash    string `gorm:"column:applied_hash;type:varchar(64);not null;default:''"`
	CreatedTime    int64  `gorm:"column:created_time;not null"`
	UpdatedTime    int64  `gorm:"column:updated_time;not null"`
}

func (NodePortMux) TableName() string { return "node_port_mux" }

// RemoteSource is a subscription URL (an airport, a friend's panel) whose
// nodes are kept as imported nodes and refreshed periodically.
type RemoteSource struct {
	ID   int64  `gorm:"primaryKey;autoIncrement"`
	Name string `gorm:"type:varchar(100);not null"`
	URL  string `gorm:"column:url;type:text;not null"`
	// IntervalHours between refreshes; 0 refreshes only by hand.
	IntervalHours int `gorm:"column:interval_hours;not null;default:0"`
	// RelayOnly marks its nodes relay-only when they are created.
	RelayOnly int `gorm:"column:relay_only;not null;default:0"`
	// Kind is RemoteSourceURL (fetched from URL) or RemoteSourceFile
	// (uploaded; updated by uploading again).
	Kind string `gorm:"type:varchar(10);not null;default:'url'"`
	// Role is what its nodes are for in a chain: "" (plain nodes),
	// "relay" (the hop in front of a landing node) or "landing".
	Role string `gorm:"type:varchar(10);not null;default:''"`
	// NameOverrides stores identity-keyed administrator names from the
	// import preview. Source refreshes keep those names stable while provider
	// display names change.
	NameOverrides      string `gorm:"column:name_overrides;type:text;not null;default:''"`
	ExcludedIdentities string `gorm:"column:excluded_identities;type:text;not null;default:''"`
	Enabled            int    `gorm:"not null;default:0"`
	LastFetch          int64  `gorm:"column:last_fetch;not null;default:0"`
	LastError          string `gorm:"column:last_error;type:text;not null;default:''"`
	NodeCount          int    `gorm:"column:node_count;not null;default:0"`
	// Userinfo is the provider's Subscription-Userinfo header (traffic and
	// expiry of the airport plan), shown as is.
	Userinfo    string `gorm:"type:varchar(255);not null;default:''"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
	UpdatedTime int64  `gorm:"column:updated_time;not null"`
}

func (RemoteSource) TableName() string { return "remote_source" }

// RemoteSourceShield records an imported node an admin deleted by hand
// from a remote source (R10-04). A refresh must not silently resurrect
// it: the sync's create branch skips entries whose identity tuple is
// shielded for that source. The identity key is the composite of the
// same name/address keys the sync matches existing rows by (see
// handler importedIdentityKey), so renaming or moving the source entry
// makes it a new tuple that is no longer shielded. Shields are removed
// together with their source.
type RemoteSourceShield struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	SourceID    int64  `gorm:"column:source_id;not null;uniqueIndex:idx_remote_source_shield_key"`
	IdentityKey string `gorm:"column:identity_key;type:text;not null;uniqueIndex:idx_remote_source_shield_key"`
	// Name is the deleted row's display name, kept for diagnostics.
	Name        string `gorm:"type:varchar(100);not null;default:''"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
}

func (RemoteSourceShield) TableName() string { return "remote_source_shield" }

const (
	RemoteSourceURL  = "url"
	RemoteSourceFile = "file"
)

// NodeInboundCounter remembers the last cumulative nft counter value seen for
// an inbound so traffic can be accumulated as deltas. Epoch changes whenever
// the agent rebuilds its counter table, which resets the counters.
// F2-02: baselines are keyed per reporting node — a baseline only has
// meaning for the node that owns the inbound, and keying it globally
// let any node forge or suppress another node's inbound totals. The
// pre-F2-02 unique index (idx_node_inbound_counter_key, no node_id) is
// dropped by finishInboundCounterNodeScope at startup; rows written
// before the column existed carry node_id = 0 and are cleared there
// (the next sample re-establishes the baseline with delta = 0).
type NodeInboundCounter struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	NodeID      int64  `gorm:"column:node_id;not null;default:0;uniqueIndex:idx_node_inbound_counter_node_key"`
	InboundID   int64  `gorm:"column:inbound_id;not null;uniqueIndex:idx_node_inbound_counter_node_key"`
	Protocol    string `gorm:"type:varchar(10);not null;uniqueIndex:idx_node_inbound_counter_node_key"`
	Direction   string `gorm:"type:varchar(10);not null;uniqueIndex:idx_node_inbound_counter_node_key"`
	Epoch       string `gorm:"type:varchar(64);not null;default:''"`
	Bytes       int64  `gorm:"not null;default:0"`
	UpdatedTime int64  `gorm:"column:updated_time;not null"`
}

func (NodeInboundCounter) TableName() string { return "node_inbound_counter" }

// NodeInboundRelay links an inbound to a forward whose entry clients use
// instead of the landing node itself. Managed relays were created by the
// panel for this purpose and are deleted together with the inbound.
type NodeInboundRelay struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	InboundID   int64  `gorm:"column:inbound_id;not null;index"`
	ForwardID   int64  `gorm:"column:forward_id;not null;uniqueIndex"`
	Name        string `gorm:"type:varchar(100);not null;default:''"`
	ShareHost   string `gorm:"column:share_host;type:varchar(255);not null;default:''"`
	Managed     int    `gorm:"not null;default:1"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
}

func (NodeInboundRelay) TableName() string { return "node_inbound_relay" }
