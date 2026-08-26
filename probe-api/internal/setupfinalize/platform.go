package setupfinalize

import (
	"errors"
)

const (
	PlatformDebian9Systemd  = "debian-9-systemd"
	PlatformDebian10Systemd = "debian-10-systemd"
	PlatformDebian11Systemd = "debian-11-systemd"
	PlatformDebian12Systemd = "debian-12-systemd"
	PlatformDebian13Systemd = "debian-13-systemd"

	PlatformUbuntu1804Systemd = "ubuntu-18.04-systemd"
	PlatformUbuntu2004Systemd = "ubuntu-20.04-systemd"
	PlatformUbuntu2204Systemd = "ubuntu-22.04-systemd"
	PlatformUbuntu2404Systemd = "ubuntu-24.04-systemd"
	PlatformUbuntu2604Systemd = "ubuntu-26.04-systemd"

	PlatformCentOSLinux7Systemd   = "centos-linux-7-systemd"
	PlatformCentOSLinux8Systemd   = "centos-linux-8-systemd"
	PlatformCentOSStream8Systemd  = "centos-stream-8-systemd"
	PlatformCentOSStream9Systemd  = "centos-stream-9-systemd"
	PlatformCentOSStream10Systemd = "centos-stream-10-systemd"

	RuntimeABISystemdV2  = "probe-linux-systemd-v2"
	SupportedPlatformIDs = PlatformDebian9Systemd + "," + PlatformDebian10Systemd + "," +
		PlatformDebian11Systemd + "," + PlatformDebian12Systemd + "," + PlatformDebian13Systemd + "," +
		PlatformUbuntu1804Systemd + "," + PlatformUbuntu2004Systemd + "," +
		PlatformUbuntu2204Systemd + "," + PlatformUbuntu2404Systemd + "," + PlatformUbuntu2604Systemd + "," +
		PlatformCentOSLinux7Systemd + "," + PlatformCentOSLinux8Systemd + "," +
		PlatformCentOSStream8Systemd + "," + PlatformCentOSStream9Systemd + "," + PlatformCentOSStream10Systemd
)

type nginxDialect uint8

const (
	nginxDialectInvalid nginxDialect = iota
	nginxDialectClassic
	nginxDialectLegacy
	nginxDialectModern
)

// SystemdUnitProfile selects a reviewed unit hardening vocabulary. Legacy is
// intentionally limited to directives understood by systemd 219; modern is
// the current, stricter unit profile.
type SystemdUnitProfile uint8

const (
	SystemdUnitProfileInvalid SystemdUnitProfile = iota
	SystemdUnitProfileLegacy
	SystemdUnitProfileModern
)

type platformContract struct {
	id                         string
	nginxDialect               nginxDialect
	unitProfile                SystemdUnitProfile
	postgresServiceUnit        string
	psqlPath                   string
	pgIsReadyPath              string
	ssPath                     string
	certbotTimerUnit           string
	nginxNativeUnitPaths       [2]string
	setprivSupportsAmbientCaps bool
}

const (
	debianPostgresServiceUnit = "postgresql.service"
	debianPsqlPath            = "/usr/bin/psql"
	debianPGIsReadyPath       = "/usr/bin/pg_isready"
	debianSSPath              = "/bin/ss"
	debianCertbotTimerUnit    = "certbot.timer"
	debianNginxNativeUnit     = "/usr/lib/systemd/system/nginx.service"
	debianNginxNativeUnitAlt  = "/lib/systemd/system/nginx.service"

	rpmPostgresServiceUnit = "postgresql-14.service"
	rpmPsqlPath            = "/usr/pgsql-14/bin/psql"
	rpmPGIsReadyPath       = "/usr/pgsql-14/bin/pg_isready"
	rpmSSPath              = "/usr/sbin/ss"
	rpmCertbotTimerUnit    = "certbot-renew.timer"
	rpmNginxNativeUnit     = "/usr/lib/systemd/system/nginx.service"
)

func debianPlatformContract(id string, dialect nginxDialect, unitProfile SystemdUnitProfile) platformContract {
	return platformContract{
		id: id, nginxDialect: dialect, unitProfile: unitProfile,
		postgresServiceUnit: debianPostgresServiceUnit,
		psqlPath:            debianPsqlPath, pgIsReadyPath: debianPGIsReadyPath,
		ssPath: debianSSPath, certbotTimerUnit: debianCertbotTimerUnit,
		nginxNativeUnitPaths:       [2]string{debianNginxNativeUnit, debianNginxNativeUnitAlt},
		setprivSupportsAmbientCaps: true,
	}
}

func rpmPlatformContract(id string, dialect nginxDialect, unitProfile SystemdUnitProfile) platformContract {
	return platformContract{
		id: id, nginxDialect: dialect, unitProfile: unitProfile,
		postgresServiceUnit: rpmPostgresServiceUnit,
		psqlPath:            rpmPsqlPath, pgIsReadyPath: rpmPGIsReadyPath,
		ssPath: rpmSSPath, certbotTimerUnit: rpmCertbotTimerUnit,
		nginxNativeUnitPaths:       [2]string{rpmNginxNativeUnit},
		setprivSupportsAmbientCaps: id != PlatformCentOSLinux7Systemd,
	}
}

// ValidatePlatformID accepts only platform contracts compiled into this
// binary. Command paths and service names are deliberately not configurable
// through the setup environment.
func ValidatePlatformID(value string) error {
	_, err := platformContractFor(value)
	return err
}

// UnitProfileForPlatformID exposes the reviewed systemd asset profile without
// exposing the remainder of the internal platform contract.
func UnitProfileForPlatformID(value string) (SystemdUnitProfile, error) {
	contract, err := platformContractFor(value)
	if err != nil {
		return SystemdUnitProfileInvalid, err
	}
	return contract.unitProfile, nil
}

func platformContractFor(value string) (platformContract, error) {
	switch value {
	case PlatformDebian9Systemd:
		return debianPlatformContract(value, nginxDialectClassic, SystemdUnitProfileLegacy), nil
	case PlatformCentOSLinux7Systemd:
		return rpmPlatformContract(value, nginxDialectClassic, SystemdUnitProfileLegacy), nil
	case PlatformDebian10Systemd, PlatformDebian11Systemd,
		PlatformUbuntu1804Systemd, PlatformUbuntu2004Systemd:
		return debianPlatformContract(value, nginxDialectLegacy, SystemdUnitProfileLegacy), nil
	case PlatformCentOSLinux8Systemd, PlatformCentOSStream8Systemd:
		return rpmPlatformContract(value, nginxDialectLegacy, SystemdUnitProfileLegacy), nil
	case PlatformDebian12Systemd, PlatformUbuntu2204Systemd, PlatformUbuntu2404Systemd:
		return debianPlatformContract(value, nginxDialectLegacy, SystemdUnitProfileModern), nil
	case PlatformCentOSStream9Systemd:
		return rpmPlatformContract(value, nginxDialectLegacy, SystemdUnitProfileModern), nil
	case PlatformDebian13Systemd, PlatformUbuntu2604Systemd:
		return debianPlatformContract(value, nginxDialectModern, SystemdUnitProfileModern), nil
	case PlatformCentOSStream10Systemd:
		return rpmPlatformContract(value, nginxDialectModern, SystemdUnitProfileModern), nil
	default:
		return platformContract{}, errors.New("unsupported setup platform identifier")
	}
}
