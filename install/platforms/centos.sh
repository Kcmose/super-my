# CentOS Linux / CentOS Stream platform adapter for the management bootstrap.
# The exact product NAME remains part of the contract so Linux and Stream are
# never inferred from ID_LIKE or from the presence of yum/dnf.
# shellcheck shell=bash
# Adapter globals are consumed after this file is concatenated with common.sh.
# shellcheck disable=SC2034,SC2154

centos_platform_configure_signing_contract() {
    local kernel_arch
    kernel_arch="$(uname -m 2>/dev/null)" ||
        die 'the CentOS kernel architecture could not be determined'
    case "$PLATFORM_ID:$kernel_arch" in
        centos-linux-7-systemd:x86_64)
            CENTOS_BASE_KEY_FINGERPRINT='6341AB2753D78A78A7C27BB124C6A8A7F4A80EB5'
            ;;
        centos-linux-7-systemd:aarch64)
            CENTOS_BASE_KEY_FINGERPRINT='EF8F3CA66EFDF32B36CDADF76C7CB6EF305D49D6'
            ;;
        centos-linux-8-systemd:x86_64|centos-linux-8-systemd:aarch64|centos-stream-8-systemd:x86_64|centos-stream-8-systemd:aarch64|centos-stream-9-systemd:x86_64|centos-stream-9-systemd:aarch64|centos-stream-10-systemd:x86_64|centos-stream-10-systemd:aarch64)
            CENTOS_BASE_KEY_FINGERPRINT='99DB70FAE1D7CE227FB6488205B555B38483C65D'
            ;;
        *) die "the CentOS signing contract does not support $PLATFORM_ID architecture $kernel_arch" ;;
    esac
    case "$PLATFORM_RPM_EL_MAJOR" in
        7) CENTOS_EPEL_KEY_FINGERPRINT='91E97D7C4A5E96F17F3E888F6A2FAEA2352C64E5' ;;
        8) CENTOS_EPEL_KEY_FINGERPRINT='94E279EB8D8F25B21810ADF121EA45AB2F86D6A1' ;;
        9) CENTOS_EPEL_KEY_FINGERPRINT='FF8AD1344597106ECE813B918A3872BF3228467C' ;;
        10) CENTOS_EPEL_KEY_FINGERPRINT='7D8D15CBFC4E62688591FB2633D98517E37ED158' ;;
        *) die 'the EPEL signing contract is unavailable' ;;
    esac
    case "$PLATFORM_RPM_EL_MAJOR:$kernel_arch" in
        7:x86_64) CENTOS_PGDG_KEY_FINGERPRINT='F245F0BF96AC182744CAFF2E64FACE1173E3B907' ;;
        7:aarch64) CENTOS_PGDG_KEY_FINGERPRINT='C78CD9E6DA3E1F5B5B16FC1A9FCD879F55B374B8' ;;
        8:x86_64|9:x86_64|10:x86_64)
            CENTOS_PGDG_KEY_FINGERPRINT='D4BF08AE67A0B4C7A1DBCCD240BCA2B408B40D20'
            ;;
        8:aarch64|9:aarch64|10:aarch64)
            CENTOS_PGDG_KEY_FINGERPRINT='B031F89FC983E98262906B6E177B343BB9738825'
            ;;
        *) die 'the PGDG signing contract is unavailable' ;;
    esac
}

centos_platform_configure() {
    local version_id="$1" os_name="$2"
    case "$version_id:$os_name" in
        '7:CentOS Linux')
            configure_rpm_platform centos-linux-7-systemd yum 219 legacy classic nginx 1
            ;;
        '8:CentOS Linux')
            configure_rpm_platform centos-linux-8-systemd dnf 239 legacy legacy nginx-core 1
            ;;
        '8:CentOS Stream')
            configure_rpm_platform centos-stream-8-systemd dnf 239 legacy legacy nginx-core 1
            ;;
        '9:CentOS Stream')
            configure_rpm_platform centos-stream-9-systemd dnf 252 modern legacy nginx-core
            ;;
        '10:CentOS Stream')
            configure_rpm_platform centos-stream-10-systemd dnf 257 modern modern nginx-core
            ;;
        *)
            die "platform centos ${version_id:-unknown} ${os_name:-unknown} is not in the accepted candidate matrix; accepted candidate platform IDs: $SUPPORTED_PLATFORM_IDS"
            ;;
    esac
    centos_platform_configure_signing_contract
}

centos_platform_preflight_commands() {
    printf '%s\n' groupadd useradd "$PLATFORM_PACKAGE_MANAGER" rpm rpmkeys
}

centos_platform_native_unit_paths() {
    printf '/usr/lib/systemd/system/%s\n' "$1"
}

centos_platform_assert_packaged_file() {
    local file_path="$1" package_name="$2" expected_fingerprint=''
    case "$package_name" in
        postgresql14|postgresql14-server)
            expected_fingerprint="$CENTOS_PGDG_KEY_FINGERPRINT"
            ;;
        nginx)
            [[ "$PLATFORM_RPM_EL_MAJOR" == 7 ]] ||
                die 'the CentOS nginx package ownership contract is invalid for this EL generation'
            expected_fingerprint="$CENTOS_EPEL_KEY_FINGERPRINT"
            ;;
        nginx-core)
            [[ "$PLATFORM_RPM_EL_MAJOR" != 7 ]] ||
                die 'the CentOS nginx-core package ownership contract is invalid for EL7'
            expected_fingerprint="$CENTOS_BASE_KEY_FINGERPRINT"
            ;;
        *)
            die "the CentOS RPM signing-key binding is unavailable for package $package_name"
            ;;
    esac
    [[ "$expected_fingerprint" =~ ^[0-9A-F]{40}$ ]] ||
        die "the CentOS RPM signing-key binding is incomplete for package $package_name"
    centos_platform_assert_imported_rpm_key "$expected_fingerprint"
    assert_rpm_packaged_file "$file_path" "$package_name" "$expected_fingerprint"
}

centos_platform_assert_postgresql_clients() {
    [[ -x "$PLATFORM_PSQL" && ! -L "$PLATFORM_PSQL" &&
       -x "$PLATFORM_PG_ISREADY" && ! -L "$PLATFORM_PG_ISREADY" ]] ||
        die 'PostgreSQL client commands must use the reviewed PGDG 14 paths under /usr/pgsql-14/bin'
    assert_platform_packaged_file "$PLATFORM_PSQL" "$PLATFORM_POSTGRES_CLIENT_PACKAGE"
    assert_platform_packaged_file "$PLATFORM_PG_ISREADY" "$PLATFORM_POSTGRES_CLIENT_PACKAGE"
}

centos_platform_preflight_security() {
    local selinux_mode='' enforce_value=''
    if command -v getenforce >/dev/null 2>&1; then
        selinux_mode="$(getenforce 2>/dev/null)" ||
            die 'could not determine the active SELinux mode'
    elif [[ -r /sys/fs/selinux/enforce ]]; then
        IFS= read -r enforce_value < /sys/fs/selinux/enforce ||
            die 'could not read the active SELinux enforcement state'
        case "$enforce_value" in
            1) selinux_mode=Enforcing ;;
            0) selinux_mode=Permissive ;;
            *) die 'the active SELinux enforcement state is invalid' ;;
        esac
    else
        selinux_mode=Disabled
    fi

    case "$selinux_mode" in
        Enforcing)
            die 'CentOS SELinux Enforcing support remains an unverified candidate; refusing before package, account, service, or permanent-path changes'
            ;;
        Permissive)
            warn 'SELinux is Permissive; this candidate may be used only for isolated compatibility testing and is not production support'
            ;;
        Disabled) ;;
        *) die "getenforce returned an unexpected mode: $selinux_mode" ;;
    esac
}

centos_platform_runtime_packages() {
    printf '%s\n' ca-certificates curl python3 certbot iproute util-linux procps-ng
}

centos_platform_write_repository() {
    local output="$1" repo_id="$2" repo_name="$3" base_url="$4"
    local key_path="$5" metadata_check="$6"
    [[ "$repo_id" =~ ^probe-[a-z0-9-]+$ && "$base_url" =~ ^https:// &&
       "$key_path" == /etc/pki/rpm-gpg/PROBE-PANEL-* &&
       ( "$metadata_check" == 0 || "$metadata_check" == 1 ) ]] ||
        die 'the CentOS managed-repository contract is invalid'
    {
        printf '[%s]\n' "$repo_id"
        printf 'name=%s\n' "$repo_name"
        printf 'baseurl=%s\n' "$base_url"
        printf 'enabled=0\n'
        printf 'gpgcheck=1\n'
        printf 'repo_gpgcheck=%s\n' "$metadata_check"
        printf 'gpgkey=file://%s\n' "$key_path"
        printf 'sslverify=1\n'
        printf 'skip_if_unavailable=0\n'
        [[ "$repo_id" != probe-pgdg14 ]] || printf 'module_hotfixes=1\n'
        printf '\n'
    } >> "$output"
}

centos_platform_assert_imported_rpm_key() {
    local fingerprint="$1" expected_key_id rpmdb_key
    [[ "$fingerprint" =~ ^[0-9A-F]{40}$ ]] ||
        die 'the imported RPM key fingerprint contract is invalid'
    [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
       -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]] ||
        die 'a private bootstrap workspace is required for RPM key validation'
    expected_key_id="${fingerprint: -8}"
    expected_key_id="${expected_key_id,,}"
    rpmdb_key="$TEMP_ROOT/rpmdb-key-$expected_key_id.asc"
    rpm -q --qf '%{DESCRIPTION}\n' "gpg-pubkey-$expected_key_id" > "$rpmdb_key" 2>/dev/null ||
        die "rpmkeys did not import the expected RPM signing key $expected_key_id"
    assert_openpgp_primary_fingerprint "$rpmdb_key" "$fingerprint" ||
        die "the imported RPM signing key $expected_key_id does not match its pinned full fingerprint"
    rm -f -- "$rpmdb_key"
}

centos_platform_capture_postgresql_module_state() {
    local mode
    [[ "$CENTOS_MODULE_STATE_CAPTURED" -eq 0 ]] ||
        die 'the PostgreSQL module state was captured more than once'
    CENTOS_MODULE_STATE_SNAPSHOT="$TEMP_ROOT/postgresql.module.before"
    if [[ -e "$CENTOS_MODULE_STATE_PATH" || -L "$CENTOS_MODULE_STATE_PATH" ]]; then
        [[ -f "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" &&
           "$(stat -c '%u:%g' "$CENTOS_MODULE_STATE_PATH")" == 0:0 ]] ||
            die "$CENTOS_MODULE_STATE_PATH must be a root-owned regular file"
        mode="$(stat -c '%a' "$CENTOS_MODULE_STATE_PATH")"
        [[ "$mode" =~ ^[0-7]{3,4}$ ]] || die 'the PostgreSQL module-state mode is invalid'
        (( (8#$mode & 8#7022) == 0 )) ||
            die "$CENTOS_MODULE_STATE_PATH must not have special bits or be writable by group or other users"
        install -o root -g root -m 0600 -- "$CENTOS_MODULE_STATE_PATH" "$CENTOS_MODULE_STATE_SNAPSHOT"
        CENTOS_MODULE_STATE_EXISTED=1
        CENTOS_MODULE_STATE_MODE="$mode"
    else
        CENTOS_MODULE_STATE_EXISTED=0
        CENTOS_MODULE_STATE_MODE=''
    fi
    CENTOS_MODULE_STATE_CAPTURED=1
}

centos_platform_cleanup_package_state() {
    local current_sha
    [[ "$CENTOS_MODULE_STATE_CAPTURED" -eq 1 ]] || return 0
    [[ "$CENTOS_MODULE_MUTATION_STARTED" -eq 1 ]] || return 0
    if [[ ! -e "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" ]]; then
        [[ "$CENTOS_MODULE_STATE_EXISTED" -eq 0 ]] && return 0
        return 1
    fi
    [[ -f "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" &&
       "$(stat -c '%u:%g' "$CENTOS_MODULE_STATE_PATH" 2>/dev/null)" == 0:0 ]] || return 1
    current_sha="$(sha256sum "$CENTOS_MODULE_STATE_PATH" 2>/dev/null | awk '{print $1}')"
    if [[ -n "$CENTOS_MODULE_STATE_MUTATED_SHA" &&
          "$current_sha" != "$CENTOS_MODULE_STATE_MUTATED_SHA" ]]; then
        return 1
    fi
    if [[ "$CENTOS_MODULE_STATE_EXISTED" -eq 1 ]]; then
        [[ -f "$CENTOS_MODULE_STATE_SNAPSHOT" && ! -L "$CENTOS_MODULE_STATE_SNAPSHOT" &&
           "$CENTOS_MODULE_STATE_MODE" =~ ^[0-7]{3,4}$ ]] || return 1
        install -o root -g root -m "$CENTOS_MODULE_STATE_MODE" -- \
            "$CENTOS_MODULE_STATE_SNAPSHOT" "$CENTOS_MODULE_STATE_PATH" || return 1
    else
        rm -f -- "$CENTOS_MODULE_STATE_PATH" || return 1
    fi
    return 0
}

centos_platform_prepare_package_sources() {
    local rpm_arch platform_variant
    local unexpected_repo_entry
    local base_key_url base_key_sha epel_key_url epel_key_sha pgdg_key_name pgdg_key_sha
    local expected_base_fingerprint expected_epel_fingerprint expected_pgdg_fingerprint
    local base_key_download epel_key_download pgdg_key_download repo_candidate
    local baseos_url appstream_url builder_url epel_url pgdg_url
    [[ -n "$TEMP_ROOT" && "$TEMP_ROOT" == /var/tmp/probe-panel-bootstrap.* &&
       -d "$TEMP_ROOT" && ! -L "$TEMP_ROOT" ]] ||
        die 'a private bootstrap workspace is required before package-source preparation'
    if [[ "$PLATFORM_EOL" == 1 && "$ACCEPT_EOL" != 1 ]]; then
        die 'an EOL RPM package source cannot be prepared without --accept-eol'
    fi
    rpm_arch="$(rpm --eval '%{_arch}' 2>/dev/null)" || die 'could not determine the RPM architecture'
    case "$(uname -m 2>/dev/null):$rpm_arch" in
        x86_64:x86_64|aarch64:aarch64) ;;
        *) die "the managed CentOS RPM architecture is inconsistent (kernel $(uname -m 2>/dev/null || printf unknown), RPM $rpm_arch)" ;;
    esac
    platform_variant="$PLATFORM_ID:$rpm_arch"
    case "$platform_variant" in
        centos-linux-7-systemd:x86_64)
            base_key_url='https://www.centos.org/keys/RPM-GPG-KEY-CentOS-7'
            base_key_sha='8b48b04b336bd725b9e611c441c65456a4168083c4febc28e88828d8ec14827f'
            expected_base_fingerprint='6341AB2753D78A78A7C27BB124C6A8A7F4A80EB5'
            baseos_url='https://vault.centos.org/7.9.2009/os/x86_64/'
            appstream_url='https://vault.centos.org/7.9.2009/updates/x86_64/'
            builder_url='https://vault.centos.org/7.9.2009/extras/x86_64/'
            ;;
        centos-linux-7-systemd:aarch64)
            base_key_url='https://www.centos.org/keys/RPM-GPG-KEY-CentOS-7-aarch64'
            base_key_sha='a771c9556de54a8eb6e3b39d56f8e76a67413b05819159dd871b9e1ab37732b6'
            expected_base_fingerprint='EF8F3CA66EFDF32B36CDADF76C7CB6EF305D49D6'
            baseos_url='https://vault.centos.org/altarch/7.9.2009/os/aarch64/'
            appstream_url='https://vault.centos.org/altarch/7.9.2009/updates/aarch64/'
            builder_url='https://vault.centos.org/altarch/7.9.2009/extras/aarch64/'
            ;;
        centos-linux-8-systemd:*)
            baseos_url="https://vault.centos.org/8.5.2111/BaseOS/$rpm_arch/os/"
            appstream_url="https://vault.centos.org/8.5.2111/AppStream/$rpm_arch/os/"
            builder_url="https://vault.centos.org/8.5.2111/PowerTools/$rpm_arch/os/"
            ;;
        centos-stream-8-systemd:*)
            baseos_url="https://vault.centos.org/8-stream/BaseOS/$rpm_arch/os/"
            appstream_url="https://vault.centos.org/8-stream/AppStream/$rpm_arch/os/"
            builder_url="https://vault.centos.org/8-stream/PowerTools/$rpm_arch/os/"
            ;;
        centos-stream-9-systemd:*)
            baseos_url="https://mirror.stream.centos.org/9-stream/BaseOS/$rpm_arch/os/"
            appstream_url="https://mirror.stream.centos.org/9-stream/AppStream/$rpm_arch/os/"
            builder_url="https://mirror.stream.centos.org/9-stream/CRB/$rpm_arch/os/"
            ;;
        centos-stream-10-systemd:*)
            baseos_url="https://mirror.stream.centos.org/10-stream/BaseOS/$rpm_arch/os/"
            appstream_url="https://mirror.stream.centos.org/10-stream/AppStream/$rpm_arch/os/"
            builder_url="https://mirror.stream.centos.org/10-stream/CRB/$rpm_arch/os/"
            ;;
        *) die "the managed CentOS repository layout is unavailable for $platform_variant" ;;
    esac

    if [[ "$PLATFORM_RPM_EL_MAJOR" != 7 ]]; then
        base_key_url='https://www.centos.org/keys/RPM-GPG-KEY-CentOS-Official'
        base_key_sha='146059788b214d7ba0dd70c1cf21111e594c6cfde201da8a9a88fe7101be8a78'
        expected_base_fingerprint='99DB70FAE1D7CE227FB6488205B555B38483C65D'
    fi

    case "$PLATFORM_RPM_EL_MAJOR" in
        7)
            epel_key_sha='028b9accc59bab1d21f2f3f544df5469910581e728a64fd8c411a725a82300c2'
            expected_epel_fingerprint='91E97D7C4A5E96F17F3E888F6A2FAEA2352C64E5'
            epel_url="https://dl.fedoraproject.org/pub/archive/epel/7/$rpm_arch/"
            ;;
        8)
            epel_key_sha='cd1db21a863185127f2e3b264c97fb1c6c44c316385707999041ea475c110d1c'
            expected_epel_fingerprint='94E279EB8D8F25B21810ADF121EA45AB2F86D6A1'
            case "$PLATFORM_ID" in
                centos-linux-8-systemd)
                    epel_url="https://dl.fedoraproject.org/pub/archive/epel/8.5/Everything/$rpm_arch/"
                    ;;
                centos-stream-8-systemd)
                    epel_url="https://dl.fedoraproject.org/pub/archive/epel/8.9/Everything/$rpm_arch/"
                    ;;
                *) die 'the EL8 EPEL snapshot contract is unavailable' ;;
            esac
            ;;
        9)
            epel_key_sha='fcf0eab4f05a1c0de6363ac4b707600a27a9d774e9b491059e59e6921b255a84'
            expected_epel_fingerprint='FF8AD1344597106ECE813B918A3872BF3228467C'
            epel_url="https://dl.fedoraproject.org/pub/epel/9/Everything/$rpm_arch/"
            ;;
        10)
            epel_key_sha='de390fc168eae5ab2852e9e93d34a0b9ddf05cf9ce90ee28d97de26a4b1f6b93'
            expected_epel_fingerprint='7D8D15CBFC4E62688591FB2633D98517E37ED158'
            epel_url="https://dl.fedoraproject.org/pub/epel/10/Everything/$rpm_arch/"
            ;;
        *) die 'the managed EPEL source generation is unavailable' ;;
    esac
    epel_key_url="https://dl.fedoraproject.org/pub/epel/RPM-GPG-KEY-EPEL-$PLATFORM_RPM_EL_MAJOR"

    case "$PLATFORM_RPM_EL_MAJOR:$rpm_arch" in
        7:x86_64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-RHEL7
            pgdg_key_sha=a18e7cea1aa78189e36f28ca9ebf293826a407a923a75ab4a6ecbc9ad5217f49
            expected_pgdg_fingerprint=F245F0BF96AC182744CAFF2E64FACE1173E3B907
            ;;
        7:aarch64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-AARCH64-RHEL7
            pgdg_key_sha=905462fba9a7755554e3762e93a5e728ba5607e6500a4b212d4eb338a9fa2c8d
            expected_pgdg_fingerprint=C78CD9E6DA3E1F5B5B16FC1A9FCD879F55B374B8
            ;;
        8:x86_64|9:x86_64|10:x86_64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-RHEL
            pgdg_key_sha=a70c9527426017d00fa4e6f9d2941d515357a27a7be82e155248ece53bbe5453
            expected_pgdg_fingerprint=D4BF08AE67A0B4C7A1DBCCD240BCA2B408B40D20
            ;;
        8:aarch64|9:aarch64|10:aarch64)
            pgdg_key_name=PGDG-RPM-GPG-KEY-AARCH64-RHEL
            pgdg_key_sha=cc506fa92aa97e8e58f88551a2ec99a61d9d603f7f2c1ae0c06191f58c29979f
            expected_pgdg_fingerprint=B031F89FC983E98262906B6E177B343BB9738825
            ;;
        *) die "the PGDG RPM source does not support EL ${PLATFORM_RPM_EL_MAJOR:-unknown} architecture $rpm_arch" ;;
    esac
    [[ "$CENTOS_BASE_KEY_FINGERPRINT" == "$expected_base_fingerprint" &&
       "$CENTOS_EPEL_KEY_FINGERPRINT" == "$expected_epel_fingerprint" &&
       "$CENTOS_PGDG_KEY_FINGERPRINT" == "$expected_pgdg_fingerprint" ]] ||
        die 'the preflight and package-source signing contracts disagree'
    pgdg_url="https://download.postgresql.org/pub/repos/yum/14/redhat/rhel-${PLATFORM_RPM_EL_MAJOR}-$rpm_arch"

    ensure_package_source_directory /etc/pki/rpm-gpg
    ensure_package_source_directory /etc/yum.repos.d
    ensure_package_source_directory "$CENTOS_MANAGED_REPO_DIR"
    unexpected_repo_entry="$(find "$CENTOS_MANAGED_REPO_DIR" -mindepth 1 -maxdepth 1 \
        ! -name 'probe-panel-runtime.repo' -print -quit)" ||
        die 'the isolated CentOS repository directory could not be inspected'
    [[ -z "$unexpected_repo_entry" ]] ||
        die "the isolated CentOS repository directory contains an unmanaged entry: $unexpected_repo_entry"
    if [[ "$PLATFORM_RPM_EL_MAJOR" == 8 || "$PLATFORM_RPM_EL_MAJOR" == 9 ]]; then
        ensure_package_source_directory /etc/dnf/modules.d
    fi
    base_key_download="$TEMP_ROOT/centos-signing-key.asc"
    epel_key_download="$TEMP_ROOT/epel-signing-key.asc"
    pgdg_key_download="$TEMP_ROOT/$pgdg_key_name"
    download_fixed_openpgp_key \
        "$base_key_url" "$base_key_sha" "$CENTOS_BASE_KEY_FINGERPRINT" "$base_key_download"
    download_fixed_openpgp_key \
        "$epel_key_url" "$epel_key_sha" "$CENTOS_EPEL_KEY_FINGERPRINT" "$epel_key_download"
    download_fixed_openpgp_key \
        "https://download.postgresql.org/pub/repos/yum/keys/$pgdg_key_name" \
        "$pgdg_key_sha" "$CENTOS_PGDG_KEY_FINGERPRINT" "$pgdg_key_download"
    install_managed_package_source "$base_key_download" "$CENTOS_BASE_KEY_PATH" 644
    install_managed_package_source "$epel_key_download" "$CENTOS_EPEL_KEY_PATH" 644
    install_managed_package_source "$pgdg_key_download" "$PGDG_RPM_KEY_PATH" 644

    repo_candidate="$TEMP_ROOT/probe-panel-runtime.repo"
    : > "$repo_candidate"
    centos_platform_write_repository "$repo_candidate" probe-centos-baseos \
        'Probe Panel pinned CentOS BaseOS' "$baseos_url" "$CENTOS_BASE_KEY_PATH" 1
    centos_platform_write_repository "$repo_candidate" probe-centos-appstream \
        'Probe Panel pinned CentOS AppStream or Updates' "$appstream_url" "$CENTOS_BASE_KEY_PATH" 1
    centos_platform_write_repository "$repo_candidate" probe-centos-builder \
        'Probe Panel pinned CentOS PowerTools, CRB or Extras' "$builder_url" "$CENTOS_BASE_KEY_PATH" 1
    # Fedora EPEL does not publish detached repomd.xml signatures. Its metadata
    # remains TLS-protected and every RPM is still required to match the exact
    # EPEL key pinned above; repo_gpgcheck must not be falsely enabled here.
    centos_platform_write_repository "$repo_candidate" probe-epel \
        'Probe Panel pinned EPEL' "$epel_url" "$CENTOS_EPEL_KEY_PATH" 0
    centos_platform_write_repository "$repo_candidate" probe-pgdg14 \
        "Probe Panel pinned PostgreSQL 14 for EL $PLATFORM_RPM_EL_MAJOR" \
        "$pgdg_url" "$PGDG_RPM_KEY_PATH" 1
    chmod 0644 "$repo_candidate"
    install_managed_package_source "$repo_candidate" "$CENTOS_MANAGED_REPO_PATH" 644
    # Importing keys mutates the RPM database. Commit the complete isolated
    # repository/key set first so every consumed package retains its immutable
    # update source if a later operation fails.
    PACKAGE_SOURCE_CONSUMED=1
    rpmkeys --import "$CENTOS_BASE_KEY_PATH" || die 'rpmkeys could not import the pinned CentOS signing key'
    rpmkeys --import "$CENTOS_EPEL_KEY_PATH" || die 'rpmkeys could not import the pinned EPEL signing key'
    rpmkeys --import "$PGDG_RPM_KEY_PATH" || die 'rpmkeys could not import the pinned PGDG signing key'
    centos_platform_assert_imported_rpm_key "$CENTOS_BASE_KEY_FINGERPRINT"
    centos_platform_assert_imported_rpm_key "$CENTOS_EPEL_KEY_FINGERPRINT"
    centos_platform_assert_imported_rpm_key "$CENTOS_PGDG_KEY_FINGERPRINT"
}

centos_platform_install_packages() {
    local -a repository_options=(
        --noplugins
        "--setopt=reposdir=$CENTOS_MANAGED_REPO_DIR"
        --disablerepo='*'
        "--enablerepo=$CENTOS_REPO_ALLOWLIST"
    )
    if [[ "$PLATFORM_PACKAGE_MANAGER" == dnf ]]; then
        dnf "${repository_options[@]}" --setopt=gpgcheck=True makecache
        if [[ "$PLATFORM_RPM_EL_MAJOR" == 8 || "$PLATFORM_RPM_EL_MAJOR" == 9 ]]; then
            centos_platform_capture_postgresql_module_state
            PACKAGE_SOURCE_CONSUMED=1
            CENTOS_MODULE_MUTATION_STARTED=1
            dnf "${repository_options[@]}" --setopt=gpgcheck=True module disable -y postgresql
            [[ -f "$CENTOS_MODULE_STATE_PATH" && ! -L "$CENTOS_MODULE_STATE_PATH" &&
               "$(stat -c '%u:%g' "$CENTOS_MODULE_STATE_PATH")" == 0:0 ]] ||
                die 'dnf did not create a safe PostgreSQL module-state file'
            CENTOS_MODULE_STATE_MUTATED_SHA="$(sha256sum "$CENTOS_MODULE_STATE_PATH" | awk '{print $1}')"
            [[ "$CENTOS_MODULE_STATE_MUTATED_SHA" =~ ^[0-9a-f]{64}$ ]] ||
                die 'the disabled PostgreSQL module state could not be hashed'
        fi
        PACKAGE_SOURCE_CONSUMED=1
        dnf "${repository_options[@]}" install -y --setopt=install_weak_deps=False \
            --setopt=gpgcheck=True --setopt=keepcache=True "$@"
    elif [[ "$PLATFORM_PACKAGE_MANAGER" == yum ]]; then
        yum "${repository_options[@]}" --setopt=gpgcheck=1 makecache
        PACKAGE_SOURCE_CONSUMED=1
        yum "${repository_options[@]}" install -y --setopt=gpgcheck=1 --setopt=keepcache=1 "$@"
    else
        die 'the CentOS package-manager contract is unavailable'
    fi
}

centos_platform_initialize_postgresql() {
    local preexisting="$1"
    [[ "$preexisting" == 0 || "$preexisting" == 1 ]] ||
        die 'the PostgreSQL preexistence state is invalid'
    [[ "$preexisting" == 0 ]] || return 0

    local pg_data_root=/var/lib/pgsql/14/data
    local pg_setup=/usr/pgsql-14/bin/postgresql-14-setup
    [[ -x "$pg_setup" && ! -L "$pg_setup" ]] ||
        die 'the reviewed PGDG 14 cluster initializer is missing'
    assert_platform_packaged_file "$pg_setup" "$PLATFORM_POSTGRES_SERVER_PACKAGE"
    if [[ ! -f "$pg_data_root/PG_VERSION" ]]; then
        if [[ -d "$pg_data_root" ]] && find "$pg_data_root" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
            die "$pg_data_root is a partial PostgreSQL cluster; refusing to initialize over it"
        fi
        "$pg_setup" initdb
    fi
    [[ -f "$pg_data_root/PG_VERSION" && ! -L "$pg_data_root/PG_VERSION" &&
       "$(tr -d '[:space:]' < "$pg_data_root/PG_VERSION")" == 14 ]] ||
        die 'the initialized PGDG cluster is not PostgreSQL 14'
}

centos_platform_create_service_account() {
    require_command groupadd
    require_command useradd
    groupadd --system probe-api
    useradd --system --gid probe-api --home-dir /nonexistent --no-create-home \
        --shell /sbin/nologin probe-api
}

centos_platform_validate_nologin_shell() {
    [[ "$1" == /sbin/nologin || "$1" == /usr/sbin/nologin ]]
}

centos_platform_disable_default_nginx_site() {
    :
}
