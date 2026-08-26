# Ubuntu platform adapter for the management bootstrap.
# Ubuntu remains a separate adapter even though it intentionally reuses the
# reviewed deb-family package and account helpers from install/common.sh.
# shellcheck shell=bash
# Adapter globals are consumed after this file is concatenated with common.sh.
# shellcheck disable=SC2034,SC2154

ubuntu_platform_configure() {
    local version_id="$1"
    case "$version_id" in
        18.04)
            configure_deb_platform ubuntu-18.04-systemd 237 legacy nginx-core nginx-core legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=bionic
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        20.04)
            configure_deb_platform ubuntu-20.04-systemd 245 legacy nginx-core nginx-core legacy postgresql-14 postgresql-client-14 1
            PLATFORM_APT_CODENAME=focal
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'
            ;;
        22.04)
            configure_deb_platform ubuntu-22.04-systemd 249 modern nginx-core nginx-core legacy postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=jammy
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        24.04)
            configure_deb_platform ubuntu-24.04-systemd 255 modern nginx nginx legacy postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=noble
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        26.04)
            configure_deb_platform ubuntu-26.04-systemd 259 modern nginx nginx modern postgresql-14 postgresql-client-14
            PLATFORM_APT_CODENAME=resolute
            PLATFORM_APT_BASE_MODE=ubuntu-live
            PLATFORM_PGDG_APT_BASE_URL='https://apt.postgresql.org/pub/repos/apt'
            ;;
        *)
            die "platform ubuntu ${version_id:-unknown} is not in the accepted candidate matrix; accepted candidate platform IDs: $SUPPORTED_PLATFORM_IDS"
            ;;
    esac
}

ubuntu_platform_preflight_commands() { deb_family_platform_preflight_commands; }
ubuntu_platform_native_unit_paths() { deb_family_platform_native_unit_paths "$@"; }
ubuntu_platform_assert_packaged_file() { deb_family_platform_assert_packaged_file "$@"; }
ubuntu_platform_assert_postgresql_clients() { deb_family_platform_assert_postgresql_clients; }
ubuntu_platform_preflight_security() { deb_family_platform_preflight_security; }
ubuntu_platform_runtime_packages() { deb_family_platform_runtime_packages; }
ubuntu_platform_prepare_package_sources() { deb_family_platform_prepare_package_sources; }
ubuntu_platform_install_packages() { deb_family_platform_install_packages "$@"; }
ubuntu_platform_initialize_postgresql() { deb_family_platform_initialize_postgresql "$@"; }
ubuntu_platform_create_service_account() { deb_family_platform_create_service_account; }
ubuntu_platform_validate_nologin_shell() { deb_family_platform_validate_nologin_shell "$@"; }
ubuntu_platform_disable_default_nginx_site() { deb_family_platform_disable_default_nginx_site; }
