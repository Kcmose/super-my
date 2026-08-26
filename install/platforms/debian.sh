# Debian platform adapter for the management bootstrap.
# This file is concatenated into the public standalone install.sh; it must only
# define functions and must never perform work while being parsed.
# shellcheck shell=bash
# Adapter globals are consumed after this file is concatenated with common.sh.
# shellcheck disable=SC2034,SC2154

debian_platform_configure() {
    local version_id="$1"
    case "$version_id" in
        9)
            configure_deb_platform debian-9-systemd 232 legacy nginx-full nginx-full classic postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=stretch
            PLATFORM_APT_BASE_MODE=debian-archive
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        10)
            configure_deb_platform debian-10-systemd 241 legacy nginx-full nginx-full legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=buster
            PLATFORM_APT_BASE_MODE=debian-archive
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        11)
            configure_deb_platform debian-11-systemd 247 legacy nginx-core nginx-core legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=bullseye
            PLATFORM_APT_BASE_MODE=debian-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        12)
            configure_deb_platform debian-12-systemd 252 modern nginx nginx legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=bookworm
            PLATFORM_APT_BASE_MODE=debian-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        13)
            configure_deb_platform debian-13-systemd 257 modern nginx nginx modern postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=trixie
            PLATFORM_APT_BASE_MODE=debian-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        *)
            die "platform debian ${version_id:-unknown} is not in the accepted candidate matrix; accepted candidate platform IDs: $SUPPORTED_PLATFORM_IDS"
            ;;
    esac
}

debian_platform_preflight_commands() { deb_family_platform_preflight_commands; }
debian_platform_native_unit_paths() { deb_family_platform_native_unit_paths "$@"; }
debian_platform_assert_packaged_file() { deb_family_platform_assert_packaged_file "$@"; }
debian_platform_assert_postgresql_clients() { deb_family_platform_assert_postgresql_clients; }
debian_platform_preflight_security() { deb_family_platform_preflight_security; }
debian_platform_runtime_packages() { deb_family_platform_runtime_packages; }
debian_platform_prepare_package_sources() { deb_family_platform_prepare_package_sources; }
debian_platform_install_packages() { deb_family_platform_install_packages "$@"; }
debian_platform_initialize_postgresql() { deb_family_platform_initialize_postgresql "$@"; }
debian_platform_create_service_account() { deb_family_platform_create_service_account; }
debian_platform_validate_nologin_shell() { deb_family_platform_validate_nologin_shell "$@"; }
debian_platform_disable_default_nginx_site() { deb_family_platform_disable_default_nginx_site; }
