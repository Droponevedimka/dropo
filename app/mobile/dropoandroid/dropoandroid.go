package dropoandroid

import (
	core "dropocore"

	"github.com/sagernet/sing-box/experimental/libbox"
)

const (
	InterfaceTypeWIFI     = libbox.InterfaceTypeWIFI
	InterfaceTypeCellular = libbox.InterfaceTypeCellular
	InterfaceTypeEthernet = libbox.InterfaceTypeEthernet
	InterfaceTypeOther    = libbox.InterfaceTypeOther
)

func EnsureStarted(basePath, appVersion string) string {
	return core.EnsureStarted(basePath, appVersion)
}

func Shutdown() string {
	return core.Shutdown()
}

func Status() string {
	return core.Status()
}

func Logs() string {
	return core.Logs()
}

func Events(since int64) string {
	return core.Events(since)
}

func SetConnected(connected bool) string {
	return core.SetConnected(connected)
}

func Call(method, argsJSON string) string {
	return core.Call(method, argsJSON)
}

func BuildSingBoxConfig() string {
	return core.BuildSingBoxConfig()
}

func Setup(options *SetupOptions) error {
	return libbox.Setup(&libbox.SetupOptions{
		BasePath:                options.BasePath,
		WorkingPath:             options.WorkingPath,
		TempPath:                options.TempPath,
		FixAndroidStack:         options.FixAndroidStack,
		CommandServerListenPort: options.CommandServerListenPort,
		CommandServerSecret:     options.CommandServerSecret,
		LogMaxLines:             int(options.LogMaxLines),
		Debug:                   options.Debug,
	})
}

func CheckConfig(configContent string) error {
	return libbox.CheckConfig(configContent)
}

func Version() string {
	return libbox.Version()
}

type SetupOptions struct {
	BasePath                string
	WorkingPath             string
	TempPath                string
	FixAndroidStack         bool
	CommandServerListenPort int32
	CommandServerSecret     string
	LogMaxLines             int64
	Debug                   bool
}

type CommandServer struct {
	inner *libbox.CommandServer
}

func NewCommandServer(handler CommandServerHandler, platformInterface PlatformInterface) (*CommandServer, error) {
	inner, err := libbox.NewCommandServer(
		&commandServerHandlerAdapter{inner: handler},
		&platformInterfaceAdapter{inner: platformInterface},
	)
	if err != nil {
		return nil, err
	}
	return &CommandServer{inner: inner}, nil
}

func (s *CommandServer) Start() error {
	return s.inner.Start()
}

func (s *CommandServer) Close() {
	s.inner.Close()
}

func (s *CommandServer) StartOrReloadService(configContent string, options *OverrideOptions) error {
	if options == nil {
		options = &OverrideOptions{}
	}
	return s.inner.StartOrReloadService(configContent, &libbox.OverrideOptions{
		AutoRedirect:   options.AutoRedirect,
		IncludePackage: &libboxStringIterator{inner: options.IncludePackage},
		ExcludePackage: &libboxStringIterator{inner: options.ExcludePackage},
	})
}

func (s *CommandServer) CloseService() error {
	return s.inner.CloseService()
}

func (s *CommandServer) WriteMessage(level int32, message string) {
	s.inner.WriteMessage(level, message)
}

func (s *CommandServer) SetError(message string) {
	s.inner.SetError(message)
}

func (s *CommandServer) NeedWIFIState() bool {
	return s.inner.NeedWIFIState()
}

func (s *CommandServer) NeedFindProcess() bool {
	return s.inner.NeedFindProcess()
}

func (s *CommandServer) Pause() {
	s.inner.Pause()
}

func (s *CommandServer) Wake() {
	s.inner.Wake()
}

func (s *CommandServer) ResetNetwork() {
	s.inner.ResetNetwork()
}

func (s *CommandServer) UpdateWIFIState() {
	s.inner.UpdateWIFIState()
}

type CommandServerHandler interface {
	ServiceStop() error
	ServiceReload() error
	GetSystemProxyStatus() (*SystemProxyStatus, error)
	SetSystemProxyEnabled(enabled bool) error
	WriteDebugMessage(message string)
}

type commandServerHandlerAdapter struct {
	inner CommandServerHandler
}

func (a *commandServerHandlerAdapter) ServiceStop() error {
	return a.inner.ServiceStop()
}

func (a *commandServerHandlerAdapter) ServiceReload() error {
	return a.inner.ServiceReload()
}

func (a *commandServerHandlerAdapter) GetSystemProxyStatus() (*libbox.SystemProxyStatus, error) {
	status, err := a.inner.GetSystemProxyStatus()
	if err != nil || status == nil {
		return nil, err
	}
	return &libbox.SystemProxyStatus{
		Available: status.Available,
		Enabled:   status.Enabled,
	}, nil
}

func (a *commandServerHandlerAdapter) SetSystemProxyEnabled(enabled bool) error {
	return a.inner.SetSystemProxyEnabled(enabled)
}

func (a *commandServerHandlerAdapter) WriteDebugMessage(message string) {
	a.inner.WriteDebugMessage(message)
}

type OverrideOptions struct {
	AutoRedirect   bool
	IncludePackage StringIterator
	ExcludePackage StringIterator
}

type SystemProxyStatus struct {
	Available bool
	Enabled   bool
}

type PlatformInterface interface {
	LocalDNSTransport() LocalDNSTransport
	UsePlatformAutoDetectInterfaceControl() bool
	AutoDetectInterfaceControl(fd int32) error
	OpenTun(options TunOptions) (int32, error)
	UseProcFS() bool
	FindConnectionOwner(ipProtocol int32, sourceAddress string, sourcePort int32, destinationAddress string, destinationPort int32) (*ConnectionOwner, error)
	StartDefaultInterfaceMonitor(listener InterfaceUpdateListener) error
	CloseDefaultInterfaceMonitor(listener InterfaceUpdateListener) error
	GetInterfaces() (NetworkInterfaceIterator, error)
	UnderNetworkExtension() bool
	IncludeAllNetworks() bool
	ReadWIFIState() *WIFIState
	SystemCertificates() StringIterator
	ClearDNSCache()
	SendNotification(notification *Notification) error
}

type platformInterfaceAdapter struct {
	inner PlatformInterface
}

func (a *platformInterfaceAdapter) LocalDNSTransport() libbox.LocalDNSTransport {
	transport := a.inner.LocalDNSTransport()
	if transport == nil {
		return nil
	}
	return &localDNSTransportAdapter{inner: transport}
}

func (a *platformInterfaceAdapter) UsePlatformAutoDetectInterfaceControl() bool {
	return a.inner.UsePlatformAutoDetectInterfaceControl()
}

func (a *platformInterfaceAdapter) AutoDetectInterfaceControl(fd int32) error {
	return a.inner.AutoDetectInterfaceControl(fd)
}

func (a *platformInterfaceAdapter) OpenTun(options libbox.TunOptions) (int32, error) {
	return a.inner.OpenTun(&tunOptionsAdapter{inner: options})
}

func (a *platformInterfaceAdapter) UseProcFS() bool {
	return a.inner.UseProcFS()
}

func (a *platformInterfaceAdapter) FindConnectionOwner(ipProtocol int32, sourceAddress string, sourcePort int32, destinationAddress string, destinationPort int32) (*libbox.ConnectionOwner, error) {
	owner, err := a.inner.FindConnectionOwner(ipProtocol, sourceAddress, sourcePort, destinationAddress, destinationPort)
	if err != nil || owner == nil {
		return nil, err
	}
	result := &libbox.ConnectionOwner{
		UserId:      owner.UserId,
		UserName:    owner.UserName,
		ProcessPath: owner.ProcessPath,
	}
	result.SetAndroidPackageNames(&libboxStringIterator{inner: owner.AndroidPackageNames()})
	return result, nil
}

func (a *platformInterfaceAdapter) StartDefaultInterfaceMonitor(listener libbox.InterfaceUpdateListener) error {
	return a.inner.StartDefaultInterfaceMonitor(&interfaceUpdateListenerAdapter{inner: listener})
}

func (a *platformInterfaceAdapter) CloseDefaultInterfaceMonitor(listener libbox.InterfaceUpdateListener) error {
	return a.inner.CloseDefaultInterfaceMonitor(&interfaceUpdateListenerAdapter{inner: listener})
}

func (a *platformInterfaceAdapter) GetInterfaces() (libbox.NetworkInterfaceIterator, error) {
	interfaces, err := a.inner.GetInterfaces()
	if err != nil {
		return nil, err
	}
	if interfaces == nil {
		return &libboxNetworkInterfaceIterator{}, nil
	}
	var result []*libbox.NetworkInterface
	for interfaces.HasNext() {
		item := interfaces.Next()
		if item == nil {
			continue
		}
		result = append(result, &libbox.NetworkInterface{
			Index:     item.Index,
			MTU:       item.MTU,
			Name:      item.Name,
			Addresses: &libboxStringIterator{inner: item.Addresses},
			Flags:     item.Flags,
			Type:      item.Type,
			DNSServer: &libboxStringIterator{inner: item.DNSServer},
			Metered:   item.Metered,
		})
	}
	return &libboxNetworkInterfaceIterator{items: result}, nil
}

func (a *platformInterfaceAdapter) UnderNetworkExtension() bool {
	return a.inner.UnderNetworkExtension()
}

func (a *platformInterfaceAdapter) IncludeAllNetworks() bool {
	return a.inner.IncludeAllNetworks()
}

func (a *platformInterfaceAdapter) ReadWIFIState() *libbox.WIFIState {
	state := a.inner.ReadWIFIState()
	if state == nil {
		return nil
	}
	return &libbox.WIFIState{SSID: state.SSID, BSSID: state.BSSID}
}

func (a *platformInterfaceAdapter) SystemCertificates() libbox.StringIterator {
	return &libboxStringIterator{inner: a.inner.SystemCertificates()}
}

func (a *platformInterfaceAdapter) ClearDNSCache() {
	a.inner.ClearDNSCache()
}

func (a *platformInterfaceAdapter) SendNotification(notification *libbox.Notification) error {
	return a.inner.SendNotification(&Notification{
		Identifier: notification.Identifier,
		TypeName:   notification.TypeName,
		TypeID:     notification.TypeID,
		Title:      notification.Title,
		Subtitle:   notification.Subtitle,
		Body:       notification.Body,
		OpenURL:    notification.OpenURL,
	})
}

type TunOptions interface {
	GetInet4Address() RoutePrefixIterator
	GetInet6Address() RoutePrefixIterator
	GetDNSServerAddress() (*StringBox, error)
	GetMTU() int32
	GetAutoRoute() bool
	GetStrictRoute() bool
	GetInet4RouteAddress() RoutePrefixIterator
	GetInet6RouteAddress() RoutePrefixIterator
	GetInet4RouteExcludeAddress() RoutePrefixIterator
	GetInet6RouteExcludeAddress() RoutePrefixIterator
	GetInet4RouteRange() RoutePrefixIterator
	GetInet6RouteRange() RoutePrefixIterator
	GetIncludePackage() StringIterator
	GetExcludePackage() StringIterator
	IsHTTPProxyEnabled() bool
	GetHTTPProxyServer() string
	GetHTTPProxyServerPort() int32
	GetHTTPProxyBypassDomain() StringIterator
	GetHTTPProxyMatchDomain() StringIterator
}

type tunOptionsAdapter struct {
	inner libbox.TunOptions
}

func (a *tunOptionsAdapter) GetInet4Address() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet4Address()}
}

func (a *tunOptionsAdapter) GetInet6Address() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet6Address()}
}

func (a *tunOptionsAdapter) GetDNSServerAddress() (*StringBox, error) {
	value, err := a.inner.GetDNSServerAddress()
	if err != nil || value == nil {
		return nil, err
	}
	return &StringBox{Value: value.Value}, nil
}

func (a *tunOptionsAdapter) GetMTU() int32 {
	return a.inner.GetMTU()
}

func (a *tunOptionsAdapter) GetAutoRoute() bool {
	return a.inner.GetAutoRoute()
}

func (a *tunOptionsAdapter) GetStrictRoute() bool {
	return a.inner.GetStrictRoute()
}

func (a *tunOptionsAdapter) GetInet4RouteAddress() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet4RouteAddress()}
}

func (a *tunOptionsAdapter) GetInet6RouteAddress() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet6RouteAddress()}
}

func (a *tunOptionsAdapter) GetInet4RouteExcludeAddress() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet4RouteExcludeAddress()}
}

func (a *tunOptionsAdapter) GetInet6RouteExcludeAddress() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet6RouteExcludeAddress()}
}

func (a *tunOptionsAdapter) GetInet4RouteRange() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet4RouteRange()}
}

func (a *tunOptionsAdapter) GetInet6RouteRange() RoutePrefixIterator {
	return &routePrefixIteratorAdapter{inner: a.inner.GetInet6RouteRange()}
}

func (a *tunOptionsAdapter) GetIncludePackage() StringIterator {
	return &stringIteratorAdapter{inner: a.inner.GetIncludePackage()}
}

func (a *tunOptionsAdapter) GetExcludePackage() StringIterator {
	return &stringIteratorAdapter{inner: a.inner.GetExcludePackage()}
}

func (a *tunOptionsAdapter) IsHTTPProxyEnabled() bool {
	return a.inner.IsHTTPProxyEnabled()
}

func (a *tunOptionsAdapter) GetHTTPProxyServer() string {
	return a.inner.GetHTTPProxyServer()
}

func (a *tunOptionsAdapter) GetHTTPProxyServerPort() int32 {
	return a.inner.GetHTTPProxyServerPort()
}

func (a *tunOptionsAdapter) GetHTTPProxyBypassDomain() StringIterator {
	return &stringIteratorAdapter{inner: a.inner.GetHTTPProxyBypassDomain()}
}

func (a *tunOptionsAdapter) GetHTTPProxyMatchDomain() StringIterator {
	return &stringIteratorAdapter{inner: a.inner.GetHTTPProxyMatchDomain()}
}

type RoutePrefix struct {
	inner *libbox.RoutePrefix
}

func (p *RoutePrefix) Address() string {
	return p.inner.Address()
}

func (p *RoutePrefix) Prefix() int32 {
	return p.inner.Prefix()
}

func (p *RoutePrefix) Mask() string {
	return p.inner.Mask()
}

func (p *RoutePrefix) String() string {
	return p.inner.String()
}

type RoutePrefixIterator interface {
	HasNext() bool
	Next() *RoutePrefix
}

type routePrefixIteratorAdapter struct {
	inner libbox.RoutePrefixIterator
}

func (a *routePrefixIteratorAdapter) HasNext() bool {
	return a.inner != nil && a.inner.HasNext()
}

func (a *routePrefixIteratorAdapter) Next() *RoutePrefix {
	if a.inner == nil {
		return nil
	}
	return &RoutePrefix{inner: a.inner.Next()}
}

type StringBox struct {
	Value string
}

type StringIterator interface {
	Len() int32
	HasNext() bool
	Next() string
}

type stringIteratorAdapter struct {
	inner libbox.StringIterator
}

func (a *stringIteratorAdapter) Len() int32 {
	if a.inner == nil {
		return 0
	}
	return a.inner.Len()
}

func (a *stringIteratorAdapter) HasNext() bool {
	return a.inner != nil && a.inner.HasNext()
}

func (a *stringIteratorAdapter) Next() string {
	if a.inner == nil {
		return ""
	}
	return a.inner.Next()
}

type libboxStringIterator struct {
	inner StringIterator
}

func (a *libboxStringIterator) Len() int32 {
	if a.inner == nil {
		return 0
	}
	return a.inner.Len()
}

func (a *libboxStringIterator) HasNext() bool {
	return a.inner != nil && a.inner.HasNext()
}

func (a *libboxStringIterator) Next() string {
	if a.inner == nil {
		return ""
	}
	return a.inner.Next()
}

type ConnectionOwner struct {
	UserId              int32
	UserName            string
	ProcessPath         string
	androidPackageNames []string
}

func (c *ConnectionOwner) SetAndroidPackageNames(names StringIterator) {
	c.androidPackageNames = collectStrings(names)
}

func (c *ConnectionOwner) AndroidPackageNames() StringIterator {
	return &sliceStringIterator{values: append([]string(nil), c.androidPackageNames...)}
}

type InterfaceUpdateListener interface {
	UpdateDefaultInterface(interfaceName string, interfaceIndex int32, isExpensive bool, isConstrained bool)
}

type interfaceUpdateListenerAdapter struct {
	inner libbox.InterfaceUpdateListener
}

func (a *interfaceUpdateListenerAdapter) UpdateDefaultInterface(interfaceName string, interfaceIndex int32, isExpensive bool, isConstrained bool) {
	a.inner.UpdateDefaultInterface(interfaceName, interfaceIndex, isExpensive, isConstrained)
}

type NetworkInterface struct {
	Index     int32
	MTU       int32
	Name      string
	Addresses StringIterator
	Flags     int32
	Type      int32
	DNSServer StringIterator
	Metered   bool
}

type NetworkInterfaceIterator interface {
	HasNext() bool
	Next() *NetworkInterface
}

type libboxNetworkInterfaceIterator struct {
	items []*libbox.NetworkInterface
	index int
}

func (i *libboxNetworkInterfaceIterator) HasNext() bool {
	return i.index < len(i.items)
}

func (i *libboxNetworkInterfaceIterator) Next() *libbox.NetworkInterface {
	if !i.HasNext() {
		return nil
	}
	item := i.items[i.index]
	i.index++
	return item
}

type WIFIState struct {
	SSID  string
	BSSID string
}

type Notification struct {
	Identifier string
	TypeName   string
	TypeID     int32
	Title      string
	Subtitle   string
	Body       string
	OpenURL    string
}

type LocalDNSTransport interface {
	Raw() bool
	Lookup(ctx *ExchangeContext, network string, domain string) error
	Exchange(ctx *ExchangeContext, message []byte) error
}

type ExchangeContext struct {
	inner *libbox.ExchangeContext
}

func (c *ExchangeContext) Success(result string) {
	c.inner.Success(result)
}

func (c *ExchangeContext) RawSuccess(result []byte) {
	c.inner.RawSuccess(result)
}

func (c *ExchangeContext) ErrorCode(code int32) {
	c.inner.ErrorCode(code)
}

func (c *ExchangeContext) ErrnoCode(code int32) {
	c.inner.ErrnoCode(code)
}

type localDNSTransportAdapter struct {
	inner LocalDNSTransport
}

func (a *localDNSTransportAdapter) Raw() bool {
	return a.inner.Raw()
}

func (a *localDNSTransportAdapter) Lookup(ctx *libbox.ExchangeContext, network string, domain string) error {
	return a.inner.Lookup(&ExchangeContext{inner: ctx}, network, domain)
}

func (a *localDNSTransportAdapter) Exchange(ctx *libbox.ExchangeContext, message []byte) error {
	return a.inner.Exchange(&ExchangeContext{inner: ctx}, message)
}

type sliceStringIterator struct {
	values []string
}

func (i *sliceStringIterator) Len() int32 {
	return int32(len(i.values))
}

func (i *sliceStringIterator) HasNext() bool {
	return len(i.values) > 0
}

func (i *sliceStringIterator) Next() string {
	if len(i.values) == 0 {
		return ""
	}
	next := i.values[0]
	i.values = i.values[1:]
	return next
}

func collectStrings(iterator StringIterator) []string {
	if iterator == nil {
		return nil
	}
	var result []string
	for iterator.HasNext() {
		result = append(result, iterator.Next())
	}
	return result
}
