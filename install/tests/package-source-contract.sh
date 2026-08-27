#!/usr/bin/env bash
# This contract deliberately searches for literal shell expressions.
# shellcheck disable=SC2016
set -Eeuo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)"
common="$root/install/common.sh"
debian="$root/install/platforms/debian.sh"
ubuntu="$root/install/platforms/ubuntu.sh"
centos="$root/install/platforms/centos.sh"

fail() {
    printf 'package-source contract: %s\n' "$*" >&2
    exit 1
}

grep -Fq "PGDG_APT_KEY_FINGERPRINT='B97B0AFCAA1A47F044F244A07FCC7D46ACCC4CF8'" "$common" ||
    fail 'the exact PGDG APT key fingerprint is not pinned'
grep -Fq "PGDG_APT_KEY_SHA256='0144068502a1eddd2a0280ede10ef607d1ec592ce819940991203941564e8e76'" "$common" ||
    fail 'the exact PGDG APT key bytes are not pinned'
grep -Fq 'assert_openpgp_primary_fingerprint' "$common" ||
    fail 'downloaded OpenPGP keys are not fingerprint-verified'
grep -Fq 'Dir::Etc::sourcelist=' "$common" ||
    fail 'APT is not isolated to the installer-managed source list'
grep -Fq 'Dir::Etc::sourceparts=-' "$common" ||
    fail 'APT source-parts isolation is missing'
grep -Fq 'check-valid-until=no' "$common" ||
    fail 'archive Valid-Until handling is missing'
grep -Fq 'https://archive.debian.org/debian-security' "$common" ||
    fail 'the Debian EOL security archive is missing'
grep -Fq 'the official archived stretch-pgdg arm64 index does not contain PostgreSQL 14' "$common" ||
    fail 'the unavailable Debian 9 arm64 cell is not failed closed'
grep -Fq 'debian9_ensure_apt_https_transport' "$common" ||
    fail 'the Debian 9 APT HTTPS bootstrap stage is missing'
bootstrap_body="$(sed -n '/^debian9_ensure_apt_https_transport()/,/^}/p' "$common")"
grep -Fq 'automatic bootstrap remains disabled until an unexpired, pinned Debian archive keyring path is formally verified' <<< "$bootstrap_body" ||
    fail 'Debian 9 must fail closed when its HTTPS transport is absent'
if grep -Eq -- 'apt-get|http://|--allow-unauthenticated|trusted=yes' <<< "$bootstrap_body"; then
    fail 'Debian 9 HTTPS transport fallback must not mutate packages or weaken transport/authentication'
fi
grep -Fq "distribution_keyring='/usr/share/keyrings/debian-archive-keyring.gpg'" "$common" ||
    fail 'Debian base repositories are not bound to the packaged Debian archive keyring'
grep -Fq "distribution_keyring_package='debian-archive-keyring'" "$common" ||
    fail 'the Debian archive keyring package ownership contract is missing'
grep -Fq "distribution_keyring='/usr/share/keyrings/ubuntu-archive-keyring.gpg'" "$common" ||
    fail 'Ubuntu base repositories are not bound to the packaged Ubuntu archive keyring'
grep -Fq "distribution_keyring_package='ubuntu-keyring'" "$common" ||
    fail 'the Ubuntu archive keyring package ownership contract is missing'
package_source_body="$(sed -n '/^deb_family_platform_prepare_package_sources()/,/^}/p' "$common")"
grep -Fq 'assert_deb_family_distribution_keyring' <<< "$package_source_body" ||
    fail 'the distribution archive keyring is not verified through its platform-specific package contract'
distribution_keyring_body="$(sed -n '/^assert_deb_family_distribution_keyring()/,/^}/p' "$common")"
grep -Fq 'debian-13-systemd:/usr/share/keyrings/debian-archive-keyring.gpg)' <<< "$distribution_keyring_body" ||
    fail 'Debian 13 is not narrowly matched for its packaged archive-keyring wrapper'
grep -Fq 'assert_deb_family_packaged_wrapper' <<< "$distribution_keyring_body" ||
    fail 'Debian 13 does not verify both the packaged archive-keyring wrapper and target'
grep -Fq '/usr/share/keyrings/debian-archive-keyring.pgp' <<< "$distribution_keyring_body" ||
    fail 'Debian 13 is not bound to the exact packaged archive-keyring target'
grep -Fq 'assert_deb_family_packaged_file "$entry_path" "$package_name"' <<< "$distribution_keyring_body" ||
    fail 'other Debian and Ubuntu releases do not retain the regular packaged-file contract'
[[ "$(grep -Ec "^[[:space:]]*printf 'deb \\[arch=%s signed-by=%s( |\\])" "$common")" -eq 8 ]] ||
    fail 'every Debian/Ubuntu base repository must use its explicit distribution Signed-By keyring'

for adapter in "$debian" "$ubuntu"; do
    if grep -E 'configure_deb_platform .* postgresql( |$)' "$adapter" >/dev/null; then
        fail "an unversioned PostgreSQL server package remains in $adapter"
    fi
    grep -Fq 'postgresql-14 postgresql-client-14' "$adapter" ||
        fail "PostgreSQL 14 package pinning is missing in $adapter"
done
grep -Fq "PLATFORM_APT_CODENAME=stretch" "$debian" || fail 'Debian 9 codename mapping is missing'
grep -Fq "PLATFORM_APT_CODENAME=trixie" "$debian" || fail 'Debian 13 codename mapping is missing'
grep -Fq "PLATFORM_APT_CODENAME=bionic" "$ubuntu" || fail 'Ubuntu 18.04 codename mapping is missing'
grep -Fq "PLATFORM_APT_CODENAME=resolute" "$ubuntu" || fail 'Ubuntu 26.04 codename mapping is missing'
[[ "$(grep -Fc "PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'" "$debian")" -eq 2 ]] ||
    fail 'Debian 9/10 must use the PGDG archive exactly'
[[ "$(grep -Fc "PLATFORM_PGDG_APT_BASE_URL='https://apt-archive.postgresql.org/pub/repos/apt'" "$ubuntu")" -eq 2 ]] ||
    fail 'Ubuntu 18.04/20.04 must use the PGDG archive exactly'

grep -Fq 'rpmkeys --import "$PGDG_RPM_KEY_PATH"' "$centos" ||
    fail 'the pinned PGDG key is not imported through rpmkeys'
repo_commit_line="$(grep -nF 'install_managed_package_source "$repo_candidate" "$CENTOS_MANAGED_REPO_PATH" 644' "$centos" | cut -d: -f1)"
consumed_line="$(grep -nF 'PACKAGE_SOURCE_CONSUMED=1' "$centos" | head -n 1 | cut -d: -f1)"
key_import_line="$(grep -nF 'rpmkeys --import "$PGDG_RPM_KEY_PATH"' "$centos" | cut -d: -f1)"
[[ "$repo_commit_line" =~ ^[0-9]+$ && "$consumed_line" =~ ^[0-9]+$ && "$key_import_line" =~ ^[0-9]+$ &&
   "$repo_commit_line" -lt "$consumed_line" && "$consumed_line" -lt "$key_import_line" ]] ||
    fail 'the complete managed RPM key/repository set must commit before the RPM DB import boundary'
grep -Fq "printf 'gpgcheck=1\\n'" "$centos" || fail 'RPM package signature checking is not mandatory'
grep -Fq "printf 'repo_gpgcheck=%s\\n'" "$centos" || fail 'RPM metadata signature policy is not explicit'
grep -Fq 'centos_platform_write_repository "$repo_candidate" probe-epel' "$centos" ||
    fail 'the isolated EPEL repository is missing'
grep -Fq "'Probe Panel pinned EPEL' \"\$epel_url\" \"\$CENTOS_EPEL_KEY_PATH\" 0" "$centos" ||
    fail 'EPEL must use TLS plus package signatures without claiming unavailable metadata signatures'
grep -Fq "'Probe Panel pinned CentOS BaseOS' \"\$baseos_url\" \"\$CENTOS_BASE_KEY_PATH\" 1" "$centos" ||
    fail 'CentOS BaseOS metadata signatures are not mandatory'
grep -Fq '"$pgdg_url" "$PGDG_RPM_KEY_PATH" 1' "$centos" ||
    fail 'PGDG metadata signatures are not mandatory'
grep -Fq "CENTOS_MANAGED_REPO_DIR='/etc/yum.repos.d/probe-panel-runtime.repos'" "$common" ||
    fail 'CentOS repositories are not isolated in an installer-only repository directory'
grep -Fq '"--setopt=reposdir=$CENTOS_MANAGED_REPO_DIR"' "$centos" ||
    fail 'yum/dnf is not restricted to the installer-managed repository directory'
grep -Fq -- "--disablerepo='*'" "$centos" || fail 'host RPM repositories are not disabled'
grep -Fq '"--enablerepo=$CENTOS_REPO_ALLOWLIST"' "$centos" ||
    fail 'the exact installer repository allowlist is missing'
grep -Fq -- '--noplugins' "$centos" || fail 'third-party yum/dnf plugins are not disabled'
grep -Fq "printf 'enabled=0\\n'" "$centos" ||
    fail 'managed repositories must remain disabled outside explicit installer invocations'
grep -Fq "! -name 'probe-panel-runtime.repo'" "$centos" ||
    fail 'the isolated repository directory does not reject unmanaged entries'
grep -Fq 'https://vault.centos.org/7.9.2009/os/x86_64/' "$centos" ||
    fail 'the CentOS 7 x86_64 Vault source is missing'
grep -Fq 'https://vault.centos.org/altarch/7.9.2009/os/aarch64/' "$centos" ||
    fail 'the CentOS 7 aarch64 Vault source is missing'
grep -Fq 'https://vault.centos.org/8.5.2111/BaseOS/' "$centos" ||
    fail 'the CentOS Linux 8 Vault source is missing'
grep -Fq 'https://dl.fedoraproject.org/pub/archive/epel/8.5/Everything/' "$centos" ||
    fail 'the CentOS Linux 8.5-matched archived EPEL source is missing'
grep -Fq 'https://dl.fedoraproject.org/pub/archive/epel/8.9/Everything/' "$centos" ||
    fail 'the CentOS Stream 8 final archived EPEL source is missing'
grep -Fq 'https://vault.centos.org/8-stream/BaseOS/' "$centos" ||
    fail 'the EOL CentOS Stream 8 Vault source is missing'
for stream in 9 10; do
    grep -Fq "https://mirror.stream.centos.org/$stream-stream/BaseOS/" "$centos" ||
        fail "the CentOS Stream $stream BaseOS source is missing"
done
centos8_key_block="$(sed -n '/if \[\[ "\$PLATFORM_RPM_EL_MAJOR" != 7 \]\]; then/,/^    fi$/p' "$centos")"
grep -Fq "base_key_url='https://www.centos.org/keys/RPM-GPG-KEY-CentOS-Official'" <<< "$centos8_key_block" ||
    fail 'the CentOS 8+ signing key must use the verified official no-suffix URL'
grep -Fq "base_key_sha='146059788b214d7ba0dd70c1cf21111e594c6cfde201da8a9a88fe7101be8a78'" <<< "$centos8_key_block" ||
    fail 'the CentOS 8+ official key URL is not paired with its exact verified bytes'
grep -Fq "expected_base_fingerprint='99DB70FAE1D7CE227FB6488205B555B38483C65D'" <<< "$centos8_key_block" ||
    fail 'the CentOS 8+ official key URL is not paired with its full fingerprint'
if grep -Fq 'RPM-GPG-KEY-CentOS-Official-SHA256' "$centos"; then
    fail 'the mismatched CentOS 8+ armored-key URL variant must not return'
fi
for key_material in \
    '8b48b04b336bd725b9e611c441c65456a4168083c4febc28e88828d8ec14827f' \
    'a771c9556de54a8eb6e3b39d56f8e76a67413b05819159dd871b9e1ab37732b6' \
    '146059788b214d7ba0dd70c1cf21111e594c6cfde201da8a9a88fe7101be8a78' \
    '028b9accc59bab1d21f2f3f544df5469910581e728a64fd8c411a725a82300c2' \
    'cd1db21a863185127f2e3b264c97fb1c6c44c316385707999041ea475c110d1c' \
    'fcf0eab4f05a1c0de6363ac4b707600a27a9d774e9b491059e59e6921b255a84' \
    'de390fc168eae5ab2852e9e93d34a0b9ddf05cf9ce90ee28d97de26a4b1f6b93'; do
    grep -Fq "$key_material" "$centos" || fail "a pinned CentOS/EPEL key hash is missing: $key_material"
done
for key_fingerprint in \
    '6341AB2753D78A78A7C27BB124C6A8A7F4A80EB5' \
    'EF8F3CA66EFDF32B36CDADF76C7CB6EF305D49D6' \
    '99DB70FAE1D7CE227FB6488205B555B38483C65D' \
    '91E97D7C4A5E96F17F3E888F6A2FAEA2352C64E5' \
    '94E279EB8D8F25B21810ADF121EA45AB2F86D6A1' \
    'FF8AD1344597106ECE813B918A3872BF3228467C' \
    '7D8D15CBFC4E62688591FB2633D98517E37ED158'; do
    grep -Fq "$key_fingerprint" "$centos" ||
        fail "a pinned CentOS/EPEL full key fingerprint is missing: $key_fingerprint"
done
grep -Fq 'dnf "${repository_options[@]}" --setopt=gpgcheck=True module disable -y postgresql' "$centos" ||
    fail 'EL8/EL9 PostgreSQL module filtering is not disabled'
grep -Fq '[[ "$PLATFORM_RPM_EL_MAJOR" == 8 || "$PLATFORM_RPM_EL_MAJOR" == 9 ]]' "$centos" ||
    fail 'PostgreSQL module disable must cover EL8 and EL9 but not EL10'
grep -Fq 'centos_platform_cleanup_package_state' "$centos" ||
    fail 'the PostgreSQL module-state rollback hook is missing'
grep -Fq 'CENTOS_MODULE_STATE_MUTATED_SHA' "$centos" ||
    fail 'PostgreSQL module-state rollback is not bound to the installer mutation'
grep -Fq 'CENTOS_MODULE_MUTATION_STARTED=1' "$centos" ||
    fail 'PostgreSQL module-state rollback does not cover an interrupted dnf mutation'
grep -Fq 'assert_rpm_packaged_file "$file_path" "$package_name" "$expected_fingerprint"' "$centos" ||
    fail 'installed CentOS runtime files are not bound to their exact source key'
grep -Fq "rpm -q --qf '%{DESCRIPTION}\\n' \"gpg-pubkey-\$expected_key_id\"" "$centos" ||
    fail 'the imported RPM trust database key is not verified by its full fingerprint'
grep -Fq 'centos_platform_configure_signing_contract' "$centos" ||
    fail 'preexisting CentOS runtimes cannot obtain their signing-key contract before source preparation'
grep -Fq 'centos_platform_cleanup_package_state' "$common" ||
    fail 'failed-install cleanup does not invoke CentOS module-state rollback'
grep -Fq 'has no signature linked to an imported trusted key' "$common" ||
    fail 'installed RPM signatures are not bound to an imported key'
grep -Fq 'PACKAGE_SOURCE_CONSUMED=1' "$common" ||
    fail 'the package-source transaction phase is missing'
grep -Fq 'preserving the managed package sources because a package transaction may have consumed them' "$common" ||
    fail 'post-consumption failure cannot preserve update sources'

printf 'package-source contract: ok\n'
