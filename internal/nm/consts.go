package nm

import "github.com/godbus/dbus/v5"

// D-Bus names. Everything bnm touches lives under these.
const (
	busName = "org.freedesktop.NetworkManager"

	pathRoot     dbus.ObjectPath = "/org/freedesktop"
	pathNM       dbus.ObjectPath = "/org/freedesktop/NetworkManager"
	pathSettings dbus.ObjectPath = "/org/freedesktop/NetworkManager/Settings"
	pathAgentMgr dbus.ObjectPath = "/org/freedesktop/NetworkManager/AgentManager"

	ifaceObjectManager = "org.freedesktop.DBus.ObjectManager"
	ifaceProperties    = "org.freedesktop.DBus.Properties"
	ifaceDBus          = "org.freedesktop.DBus"

	ifaceNM         = "org.freedesktop.NetworkManager"
	ifaceSettings   = "org.freedesktop.NetworkManager.Settings"
	ifaceConnection = "org.freedesktop.NetworkManager.Settings.Connection"
	ifaceDevice     = "org.freedesktop.NetworkManager.Device"
	ifaceWireless   = "org.freedesktop.NetworkManager.Device.Wireless"
	ifaceWired      = "org.freedesktop.NetworkManager.Device.Wired"
	ifaceWireGuard  = "org.freedesktop.NetworkManager.Device.WireGuard"
	ifaceAP         = "org.freedesktop.NetworkManager.AccessPoint"
	ifaceActive     = "org.freedesktop.NetworkManager.Connection.Active"
	ifaceVPN        = "org.freedesktop.NetworkManager.VPN.Connection"
	ifaceIP4Config  = "org.freedesktop.NetworkManager.IP4Config"
	ifaceIP6Config  = "org.freedesktop.NetworkManager.IP6Config"
)

// NMDeviceType (nm-dbus-interface.h).
const (
	devTypeUnknown   uint32 = 0
	devTypeEthernet  uint32 = 1
	devTypeWifi      uint32 = 2
	devTypeBridge    uint32 = 13
	devTypeVlan      uint32 = 11
	devTypeTun       uint32 = 16
	devTypeVeth      uint32 = 20
	devTypeWireGuard uint32 = 29
	devTypeWifiP2P   uint32 = 30 // wpa_supplicant P2P management object, not a netdev
	devTypeLoopback  uint32 = 32
)

// NMDeviceState.
const (
	devStateUnknown      uint32 = 0
	devStateUnmanaged    uint32 = 10
	devStateUnavailable  uint32 = 20
	devStateDisconnected uint32 = 30
	devStatePrepare      uint32 = 40
	devStateConfig       uint32 = 50
	devStateNeedAuth     uint32 = 60
	devStateIPConfig     uint32 = 70
	devStateIPCheck      uint32 = 80
	devStateSecondaries  uint32 = 90
	devStateActivated    uint32 = 100
	devStateDeactivating uint32 = 110
	devStateFailed       uint32 = 120
)

// NMDeviceStateReason values bnm translates into words.
const (
	devReasonNone                 uint32 = 0
	devReasonNoSecrets            uint32 = 7
	devReasonSupplicantDisconnect uint32 = 8
	devReasonSupplicantConfigFail uint32 = 9
	devReasonSupplicantFailed     uint32 = 10
	devReasonSupplicantTimeout    uint32 = 11
	devReasonSSIDNotFound         uint32 = 53
)

// NMActiveConnectionState.
const (
	activeUnknown      uint32 = 0
	activeActivating   uint32 = 1
	activeActivated    uint32 = 2
	activeDeactivating uint32 = 3
	activeDeactivated  uint32 = 4
)

// NMActiveConnectionStateReason.
const (
	activeReasonUnknown            uint32 = 0
	activeReasonNone               uint32 = 1
	activeReasonUserDisconnected   uint32 = 2
	activeReasonDeviceDisconnected uint32 = 3
	activeReasonServiceStopped     uint32 = 4
	activeReasonIPConfigInvalid    uint32 = 5
	activeReasonConnectTimeout     uint32 = 6
	activeReasonServiceStartTO     uint32 = 7
	activeReasonServiceStartFailed uint32 = 8
	activeReasonNoSecrets          uint32 = 9
	activeReasonLoginFailed        uint32 = 10
	activeReasonConnectionRemoved  uint32 = 11
	activeReasonDependencyFailed   uint32 = 12
	activeReasonDeviceRealizeFail  uint32 = 13
	activeReasonDeviceRemoved      uint32 = 14
)

// NMActivationStateFlags.
const (
	activeFlagExternal uint32 = 0x80
)

// NM80211ApFlags / NM80211ApSecurityFlags.
const (
	apFlagPrivacy uint32 = 0x1
	apFlagWPS     uint32 = 0x2

	apSecPairWEP40    uint32 = 0x1
	apSecPairWEP104   uint32 = 0x2
	apSecPairTKIP     uint32 = 0x4
	apSecPairCCMP     uint32 = 0x8
	apSecGroupWEP40   uint32 = 0x10
	apSecGroupWEP104  uint32 = 0x20
	apSecGroupTKIP    uint32 = 0x40
	apSecGroupCCMP    uint32 = 0x80
	apSecKeyMgmtPSK   uint32 = 0x100
	apSecKeyMgmt8021X uint32 = 0x200
	apSecKeyMgmtSAE   uint32 = 0x400
	apSecKeyMgmtOWE   uint32 = 0x800
	apSecKeyMgmtOWETM uint32 = 0x1000
	apSecKeyMgmtEAPB  uint32 = 0x2000
)

// NMSettingsUpdate2Flags / NMSettingsAddConnection2Flags.
const (
	updateToDisk uint32 = 0x1
)

// NMSettingSecretFlags.
const secretFlagsNone uint32 = 0

// NM connection.type values.
const (
	typeWifi      = "802-11-wireless"
	typeEthernet  = "802-3-ethernet"
	typeWireGuard = "wireguard"
	typeVPN       = "vpn"
	typeBridge    = "bridge"

	settingConnection   = "connection"
	settingWifi         = "802-11-wireless"
	settingWifiSecurity = "802-11-wireless-security"
	settingIPv4         = "ipv4"
	settingIPv6         = "ipv6"
	settingVPN          = "vpn"
	settingWireGuard    = "wireguard"
)

// Polkit action names NM reports through GetPermissions.
const (
	permNetworkControl = "org.freedesktop.NetworkManager.network-control"
	permWifiScan       = "org.freedesktop.NetworkManager.wifi.scan"
	permModifyOwn      = "org.freedesktop.NetworkManager.settings.modify.own"
	permModifySystem   = "org.freedesktop.NetworkManager.settings.modify.system"
	permEnableWifi     = "org.freedesktop.NetworkManager.enable-disable-wifi"
)
