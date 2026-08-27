#!/usr/bin/env python3
"""Strict, read-only validator for Probe Panel management v1.2.0 assets.

Usage:
    python3 .github/scripts/verify-published-management-v1.2.0.py \
        ASSET_ROOT EXPECTED_SOURCE_COMMIT EXPECTED_SOURCE_TAG_OBJECT

The validator never extracts an archive and never executes archive content.
It is deliberately release-specific: the public asset names, sizes and outer
SHA-256 digests are pinned in this file in addition to the caller-supplied Git
subject being required in each RELEASE-MANIFEST.
"""

import gzip
import hashlib
import os
import re
import stat
import sys
import tarfile
import zlib
from pathlib import PurePosixPath


VERSION = "v1.2.0"
SOURCE_REPOSITORY = "Kcmose/super-my"
RUNTIME_ABI = "probe-linux-systemd-v2"
CANONICAL_MTIME = 1577836800

MAX_ARCHIVE_BYTES = 512 * 1024 * 1024
MAX_MEMBERS = 20000
MAX_FILE_BYTES = 512 * 1024 * 1024
MAX_EXPANDED_FILE_BYTES = 2 * 1024 * 1024 * 1024
# This includes tar headers and padding, not only member payloads.
MAX_TAR_STREAM_BYTES = MAX_EXPANDED_FILE_BYTES + 256 * 1024 * 1024
MAX_MANIFEST_BYTES = 1024 * 1024
COPY_CHUNK_BYTES = 64 * 1024
DECOMPRESS_CHUNK_BYTES = 1024 * 1024

EXPECTED_OUTER_ASSETS = {
    "SHA256SUMS": (
        230,
        "3369eb411e748a66f762a24849caae99a37de6b35cfea7d8f967a44a58d1c874",
    ),
    "probe-panel-management-v1.2.0-linux-amd64.tar.gz": (
        7829818,
        "3c0638437e2bfa0a80742625c63e26a56e1699bb708dc55d7c5a199d1698b53d",
    ),
    "probe-panel-management-v1.2.0-linux-arm64.tar.gz": (
        7173356,
        "a3e3ba684113385ae2161e1313aee8928123fdc0ab3a3175e669aea50ae2c65d",
    ),
}

PLATFORM_IDS = (
    "debian-9-systemd",
    "debian-10-systemd",
    "debian-11-systemd",
    "debian-12-systemd",
    "debian-13-systemd",
    "ubuntu-18.04-systemd",
    "ubuntu-20.04-systemd",
    "ubuntu-22.04-systemd",
    "ubuntu-24.04-systemd",
    "ubuntu-26.04-systemd",
    "centos-linux-7-systemd",
    "centos-linux-8-systemd",
    "centos-stream-8-systemd",
    "centos-stream-9-systemd",
    "centos-stream-10-systemd",
)

ADMIN_FILES = {
    "artifacts/admin/assets/AuditLogs-BYcNZehn.js",
    "artifacts/admin/assets/Install-5HEfX7Bi.js",
    "artifacts/admin/assets/Install-CTYUisc5.css",
    "artifacts/admin/assets/Login-DOHVBPN6.js",
    "artifacts/admin/assets/NodeTokens-AKaSXHhF.js",
    "artifacts/admin/assets/ProbeTargets-BLV9Y2Lu.js",
    "artifacts/admin/assets/SystemStatus-DVxd2D7P.js",
    "artifacts/admin/assets/ThemeToggle-DLYIhvUG.js",
    "artifacts/admin/assets/Users-DtGmTiHm.js",
    "artifacts/admin/assets/_plugin-vue_export-helper-BDNMzG2s.js",
    "artifacts/admin/assets/admin-CxXkKodt.js",
    "artifacts/admin/assets/index-1x8x-6-E.css",
    "artifacts/admin/assets/index-DuqEHRSk.js",
    "artifacts/admin/assets/panel-DoA-JvNH.js",
    "artifacts/admin/assets/theme-BMArZXJg.js",
    "artifacts/admin/index.html",
    "artifacts/admin/theme-init.js",
}

MIGRATION_FILES = {
    "artifacts/migrations/000001_initial.down.sql",
    "artifacts/migrations/000001_initial.up.sql",
    "artifacts/migrations/000002_probe_targets_tcp_http_only.down.sql",
    "artifacts/migrations/000002_probe_targets_tcp_http_only.up.sql",
    "artifacts/migrations/000003_login_rate_limits.down.sql",
    "artifacts/migrations/000003_login_rate_limits.up.sql",
    "artifacts/migrations/000004_admin_audit_indexes.down.sql",
    "artifacts/migrations/000004_admin_audit_indexes.up.sql",
    "artifacts/migrations/000005_anonymous_guests_admin_accounts.down.sql",
    "artifacts/migrations/000005_anonymous_guests_admin_accounts.up.sql",
    "artifacts/migrations/embed.go",
}

CONFIG_FILES = {
    "source/probe-api/config/probe-api.env.example",
    "source/probe-api/config/probe-postgres-backup.env.example",
}

DEPLOY_FILES = {
    "source/probe-api/deploy/nginx/nginx-management-classic.conf",
    "source/probe-api/deploy/nginx/nginx-management-ip-classic.conf",
    "source/probe-api/deploy/nginx/nginx-management-ip-legacy.conf",
    "source/probe-api/deploy/nginx/nginx-management-ip.conf",
    "source/probe-api/deploy/nginx/nginx-management-legacy.conf",
    "source/probe-api/deploy/nginx/nginx-management.conf",
    "source/probe-api/deploy/scripts/backup-postgres.sh",
    "source/probe-api/deploy/scripts/deploy-common.sh",
    "source/probe-api/deploy/scripts/install-release.sh",
    "source/probe-api/deploy/scripts/restore-management.sh",
    "source/probe-api/deploy/scripts/restore-postgres.sh",
    "source/probe-api/deploy/scripts/uninstall-management.sh",
    "source/probe-api/deploy/scripts/validate-management.sh",
    "source/probe-api/deploy/setup/probe-panel-finalizer-management-legacy.service",
    "source/probe-api/deploy/setup/probe-panel-finalizer-management.service",
    "source/probe-api/deploy/setup/probe-panel-finalizer.path",
    "source/probe-api/deploy/setup/probe-panel-setup-legacy.service",
    "source/probe-api/deploy/setup/probe-panel-setup-legacy.socket",
    "source/probe-api/deploy/setup/probe-panel-setup.env.example",
    "source/probe-api/deploy/setup/probe-panel-setup.service",
    "source/probe-api/deploy/setup/probe-panel-setup.socket",
    "source/probe-api/deploy/systemd/probe-api-legacy.service",
    "source/probe-api/deploy/systemd/probe-api.service",
    "source/probe-api/deploy/systemd/probe-postgres-backup-legacy.service",
    "source/probe-api/deploy/systemd/probe-postgres-backup-legacy.timer",
    "source/probe-api/deploy/systemd/probe-postgres-backup.service",
    "source/probe-api/deploy/systemd/probe-postgres-backup.timer",
}

PAYLOAD_FILES = (
    ADMIN_FILES
    | MIGRATION_FILES
    | CONFIG_FILES
    | DEPLOY_FILES
    | {
        "artifacts/api/probe-api",
        "setup/probe-setup",
    }
)

ROOT_METADATA_FILES = {"BUNDLE-SHA256SUMS", "RELEASE-MANIFEST"}
EXPECTED_RELATIVE_FILES = PAYLOAD_FILES | ROOT_METADATA_FILES

EXECUTABLE_FILES = {
    "artifacts/api/probe-api",
    "setup/probe-setup",
    "source/probe-api/deploy/scripts/install-release.sh",
    "source/probe-api/deploy/scripts/restore-management.sh",
    "source/probe-api/deploy/scripts/uninstall-management.sh",
    "source/probe-api/deploy/scripts/validate-management.sh",
}

CAPTURE_FILES = (
    ADMIN_FILES
    | {
        "BUNDLE-SHA256SUMS",
        "RELEASE-MANIFEST",
        "source/probe-api/deploy/scripts/deploy-common.sh",
        "source/probe-api/deploy/scripts/install-release.sh",
    }
)

CHECKSUM_LINE = re.compile(rb"([0-9a-f]{64})  ([^\s\\]+)")
LOWER_OBJECT_ID = re.compile(r"[0-9a-f]{40}")

FORBIDDEN_DEPLOY_HELPER = re.compile(
    r"MANAGEMENT_BUNDLE_EXCLUDE"
    r"|build_release_artifacts"
    r"|deploy_release\(\)"
    r"|npm\s+run\s+build"
    r"|[.]/cmd/probe-agent"
    r"|artifacts/(?:agent|web)"
    r"|PROBE_(?:AGENT|WEB)_DIR"
    r"|old_(?:agent|web)"
    r"|probe-web"
    r"|/srv/probe/(?:agent|web)"
    r"|(?<![A-Za-z0-9_])full(?![A-Za-z0-9_])",
    re.IGNORECASE,
)

FORBIDDEN_ADMIN_CONTROLS = (
    b"panel-domain",
    b"agent-domain",
    "游客面板域名".encode("utf-8"),
    "Agent API 域名".encode("utf-8"),
    "三个域名".encode("utf-8"),
)


class ValidationError(Exception):
    pass


def reject(message):
    raise ValidationError(message)


def file_sha256(file_object):
    file_object.seek(0)
    digest = hashlib.sha256()
    while True:
        chunk = file_object.read(COPY_CHUNK_BYTES)
        if not chunk:
            break
        digest.update(chunk)
    return digest.hexdigest()


def canonical_relative_path(candidate, label):
    if not isinstance(candidate, str) or not candidate:
        reject("{} is empty or is not text".format(label))
    if not candidate.isascii():
        reject("{} is not ASCII: {!r}".format(label, candidate))
    if "\\" in candidate or "\x00" in candidate:
        reject("{} contains a forbidden character: {!r}".format(label, candidate))
    if any(ord(character) < 32 or ord(character) == 127 for character in candidate):
        reject("{} contains a control character: {!r}".format(label, candidate))
    if candidate.startswith("/") or candidate.endswith("/"):
        reject("{} is not a canonical relative path: {!r}".format(label, candidate))
    parts = candidate.split("/")
    if any(part in ("", ".", "..") for part in parts):
        reject("{} has an unsafe component: {!r}".format(label, candidate))
    if PurePosixPath(candidate).as_posix() != candidate:
        reject("{} is not canonical: {!r}".format(label, candidate))
    encoded = candidate.encode("ascii")
    if len(encoded) > 4095 or any(len(part.encode("ascii")) > 255 for part in parts):
        reject("{} exceeds the supported path length: {!r}".format(label, candidate))
    return parts


def expected_release_manifest(architecture, source_commit, source_tag_object):
    lines = (
        "format=probe-panel-release-v1",
        "version={}".format(VERSION),
        "architecture=linux-{}".format(architecture),
        "profile=management",
        "runtime_abi={}".format(RUNTIME_ABI),
        "platform_ids={}".format(",".join(PLATFORM_IDS)),
        "source_repository={}".format(SOURCE_REPOSITORY),
        "source_commit={}".format(source_commit),
        "source_tag_object={}".format(source_tag_object),
        "super_my_ref=refs/tags/{}".format(VERSION),
    )
    return ("\n".join(lines) + "\n").encode("ascii")


def expected_outer_manifest():
    lines = []
    for architecture in ("amd64", "arm64"):
        name = "probe-panel-management-v1.2.0-linux-{}.tar.gz".format(architecture)
        lines.append("{}  {}\n".format(EXPECTED_OUTER_ASSETS[name][1], name))
    return "".join(lines).encode("ascii")


def validate_single_gzip_stream(file_object, label):
    """Validate CRC/trailer and reject concatenated or trailing gzip data."""
    file_object.seek(0)
    decompressor = zlib.decompressobj(16 + zlib.MAX_WBITS)
    expanded = 0
    reached_eof = False
    while True:
        compressed = file_object.read(COPY_CHUNK_BYTES)
        if not compressed:
            break
        pending = compressed
        while pending:
            try:
                output = decompressor.decompress(pending, DECOMPRESS_CHUNK_BYTES)
            except zlib.error as error:
                reject("{} has an invalid gzip stream: {}".format(label, error))
            expanded += len(output)
            if expanded > MAX_TAR_STREAM_BYTES:
                reject("{} expands beyond the tar stream limit".format(label))
            pending = decompressor.unconsumed_tail
            if decompressor.eof:
                trailing = decompressor.unused_data + pending
                if trailing or file_object.read(1):
                    reject("{} has concatenated or trailing gzip data".format(label))
                reached_eof = True
                pending = b""
                break
        if reached_eof:
            break
    if not reached_eof or not decompressor.eof:
        reject("{} has a truncated gzip stream".format(label))


def read_member_payload(bundle, member, relative):
    source = bundle.extractfile(member)
    if source is None:
        reject("regular member has no payload: {!r}".format(member.name))
    digest = hashlib.sha256()
    capture = bytearray() if relative in CAPTURE_FILES else None
    remaining = member.size
    while remaining:
        chunk = source.read(min(COPY_CHUNK_BYTES, remaining))
        if not chunk:
            reject("member ended before its declared size: {!r}".format(member.name))
        if len(chunk) > remaining:
            reject("member exceeded its declared size: {!r}".format(member.name))
        digest.update(chunk)
        if capture is not None:
            if len(capture) > MAX_MANIFEST_BYTES - len(chunk):
                reject("captured management text exceeds 1 MiB: {!r}".format(member.name))
            capture.extend(chunk)
        remaining -= len(chunk)
    if source.read(1):
        reject("member exceeded its declared size: {!r}".format(member.name))
    source.close()
    return digest.hexdigest(), bytes(capture) if capture is not None else None


def expected_directories(root_name):
    directories = {root_name}
    for relative in EXPECTED_RELATIVE_FILES:
        parts = relative.split("/")[:-1]
        for length in range(1, len(parts) + 1):
            directories.add(root_name + "/" + "/".join(parts[:length]))
    return directories


def validate_inner_manifest(data, actual_hashes, label):
    if not data or data[-1:] != b"\n" or b"\r" in data or b"\x00" in data:
        reject("{} must be non-empty canonical LF text".format(label))
    lines = data[:-1].split(b"\n")
    records = {}
    record_order = []
    for line_number, line in enumerate(lines, 1):
        match = CHECKSUM_LINE.fullmatch(line)
        if match is None:
            reject("{} line {} is malformed".format(label, line_number))
        digest = match.group(1).decode("ascii")
        try:
            relative = match.group(2).decode("ascii")
        except UnicodeDecodeError:
            reject("{} line {} has a non-ASCII path".format(label, line_number))
        canonical_relative_path(relative, "{} path".format(label))
        if relative in records:
            reject("{} has a duplicate path: {!r}".format(label, relative))
        records[relative] = digest
        record_order.append(relative)
    if record_order != sorted(record_order):
        reject("{} is not in canonical path order".format(label))
    if set(records) != PAYLOAD_FILES:
        missing = sorted(PAYLOAD_FILES - set(records))
        extra = sorted(set(records) - PAYLOAD_FILES)
        reject("{} payload coverage mismatch; missing={!r}, extra={!r}".format(
            label, missing, extra
        ))
    if set(actual_hashes) != PAYLOAD_FILES:
        reject("{} actual payload set differs from the allowlist".format(label))
    for relative in sorted(PAYLOAD_FILES):
        if records[relative] != actual_hashes[relative]:
            reject("{} checksum mismatch for {!r}".format(label, relative))


def validate_management_content(captured, label):
    for relative in sorted(ADMIN_FILES):
        data = captured.get(relative)
        if data is None:
            reject("{} did not capture admin file {!r}".format(label, relative))
        for forbidden in FORBIDDEN_ADMIN_CONTROLS:
            if forbidden in data:
                reject("{} admin content has a historical multi-product control in {!r}".format(
                    label, relative
                ))

    helper_name = "source/probe-api/deploy/scripts/deploy-common.sh"
    helper_data = captured.get(helper_name)
    if helper_data is None:
        reject("{} did not capture deploy-common.sh".format(label))
    try:
        helper = helper_data.decode("utf-8", errors="strict")
    except UnicodeDecodeError:
        reject("{} deploy-common.sh is not UTF-8".format(label))
    if helper.count("ProtectSystem=full") != 2:
        reject("{} deploy-common.sh legacy hardening contract changed".format(label))
    sanitized = helper.replace(
        "ProtectSystem=full", "ProtectSystem=reviewed-legacy-hardening"
    )
    if FORBIDDEN_DEPLOY_HELPER.search(sanitized):
        reject("{} deploy-common.sh contains forbidden full, Agent, or visitor logic".format(label))

    install_name = "source/probe-api/deploy/scripts/install-release.sh"
    install_data = captured.get(install_name)
    if install_data is None:
        reject("{} did not capture install-release.sh".format(label))
    for token in (b"--disable-default-site", b"disable_default_nginx_site"):
        if token in helper_data or token in install_data:
            reject("{} contains forbidden default-Nginx-site mutation logic".format(label))


def validate_archive(file_object, architecture, source_commit, source_tag_object):
    asset_name = "probe-panel-management-v1.2.0-linux-{}.tar.gz".format(architecture)
    root_name = asset_name[:-7]
    label = asset_name

    validate_single_gzip_stream(file_object, label)
    file_object.seek(0)

    member_names = []
    directories = set()
    relative_files = set()
    actual_hashes = {}
    captured = {}
    expanded_file_bytes = 0

    try:
        with tarfile.open(fileobj=file_object, mode="r|gz") as bundle:
            if bundle.pax_headers:
                reject("{} has global PAX headers".format(label))
            for member in bundle:
                if len(member_names) >= MAX_MEMBERS:
                    reject("{} has more than {} logical members".format(label, MAX_MEMBERS))
                name = member.name
                parts = canonical_relative_path(name, "{} member path".format(label))
                if parts[0] != root_name:
                    reject("{} member escapes the exact archive root: {!r}".format(label, name))
                if name in directories or name in relative_files:
                    reject("{} has a duplicate logical member: {!r}".format(label, name))
                if member.pax_headers:
                    reject("{} member has PAX overrides: {!r}".format(label, name))
                if member.linkname:
                    reject("{} member has a link target: {!r}".format(label, name))
                if getattr(member, "sparse", None) is not None:
                    reject("{} member is sparse: {!r}".format(label, name))
                if member.uid != 0 or member.gid != 0:
                    reject("{} member is not owned by numeric root: {!r}".format(label, name))
                if member.uname != "" or member.gname != "":
                    reject("{} member has non-empty owner names: {!r}".format(label, name))
                if member.mtime != CANONICAL_MTIME:
                    reject("{} member has a non-canonical mtime: {!r}".format(label, name))
                member_names.append(name)

                if member.isdir():
                    if member.size != 0:
                        reject("{} directory has a payload: {!r}".format(label, name))
                    if member.mode != 0o755:
                        reject("{} directory mode is not 0755: {!r}".format(label, name))
                    directories.add(name)
                    continue

                if not member.isfile():
                    reject("{} has a link or special logical member: {!r}".format(label, name))
                if len(parts) < 2:
                    reject("{} has a file in place of its root directory".format(label))
                relative = "/".join(parts[1:])
                if relative not in EXPECTED_RELATIVE_FILES:
                    reject("{} file is outside the management-only allowlist: {!r}".format(
                        label, relative
                    ))
                full_name = root_name + "/" + relative
                if full_name in relative_files:
                    reject("{} has a duplicate file: {!r}".format(label, full_name))
                relative_files.add(full_name)
                if not isinstance(member.size, int) or member.size < 0:
                    reject("{} file has an invalid size: {!r}".format(label, relative))
                if member.size > MAX_FILE_BYTES:
                    reject("{} file exceeds 512 MiB: {!r}".format(label, relative))
                if expanded_file_bytes > MAX_EXPANDED_FILE_BYTES - member.size:
                    reject("{} payload expands beyond 2 GiB".format(label))
                expanded_file_bytes += member.size

                if relative == "BUNDLE-SHA256SUMS":
                    expected_mode = 0o600
                elif relative in EXECUTABLE_FILES:
                    expected_mode = 0o755
                else:
                    expected_mode = 0o644
                if member.mode != expected_mode:
                    reject("{} has mode {:04o}, expected {:04o}: {!r}".format(
                        label, member.mode, expected_mode, relative
                    ))

                digest, content = read_member_payload(bundle, member, relative)
                if relative in PAYLOAD_FILES:
                    actual_hashes[relative] = digest
                if content is not None:
                    captured[relative] = content
    except (tarfile.TarError, EOFError, gzip.BadGzipFile, zlib.error) as error:
        reject("{} is not a valid gzip/tar stream: {}".format(label, error))

    expected_full_files = {root_name + "/" + relative for relative in EXPECTED_RELATIVE_FILES}
    if relative_files != expected_full_files:
        missing = sorted(expected_full_files - relative_files)
        extra = sorted(relative_files - expected_full_files)
        reject("{} file set mismatch; missing={!r}, extra={!r}".format(label, missing, extra))
    expected_dirs = expected_directories(root_name)
    if directories != expected_dirs:
        missing = sorted(expected_dirs - directories)
        extra = sorted(directories - expected_dirs)
        reject("{} directory set mismatch; missing={!r}, extra={!r}".format(
            label, missing, extra
        ))
    if member_names != sorted(member_names):
        reject("{} logical members are not in canonical path order".format(label))

    release_manifest = captured.get("RELEASE-MANIFEST")
    expected_manifest = expected_release_manifest(
        architecture, source_commit, source_tag_object
    )
    if release_manifest != expected_manifest:
        reject("{} RELEASE-MANIFEST differs from the exact reviewed contract".format(label))

    bundle_manifest = captured.get("BUNDLE-SHA256SUMS")
    if bundle_manifest is None:
        reject("{} is missing BUNDLE-SHA256SUMS content".format(label))
    validate_inner_manifest(bundle_manifest, actual_hashes, label + " BUNDLE-SHA256SUMS")
    validate_management_content(captured, label)

    return {
        "members": len(member_names),
        "files": len(relative_files),
        "directories": len(directories),
        "payload_files": len(actual_hashes),
        "expanded_file_bytes": expanded_file_bytes,
    }


def open_verified_asset(asset_root, name):
    path = os.path.join(asset_root, name)
    before = os.lstat(path)
    if not stat.S_ISREG(before.st_mode) or stat.S_ISLNK(before.st_mode):
        reject("outer asset must be a non-symlink regular file: {!r}".format(name))
    expected_size, expected_digest = EXPECTED_OUTER_ASSETS[name]
    if before.st_size != expected_size:
        reject("outer asset size mismatch for {!r}".format(name))
    flags = os.O_RDONLY | getattr(os, "O_BINARY", 0) | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    file_object = os.fdopen(descriptor, "rb")
    try:
        opened = os.fstat(file_object.fileno())
        if not stat.S_ISREG(opened.st_mode) or not os.path.samestat(before, opened):
            reject("outer asset changed while opening: {!r}".format(name))
        actual_digest = file_sha256(file_object)
        if actual_digest != expected_digest:
            reject("outer asset SHA-256 mismatch for {!r}".format(name))
        file_object.seek(0)
        return file_object, before
    except Exception:
        file_object.close()
        raise


def close_verified_asset(file_object, before, name):
    try:
        after = os.fstat(file_object.fileno())
        if not os.path.samestat(before, after) or after.st_size != before.st_size:
            reject("outer asset changed during validation: {!r}".format(name))
        if file_sha256(file_object) != EXPECTED_OUTER_ASSETS[name][1]:
            reject("outer asset content changed during validation: {!r}".format(name))
    finally:
        file_object.close()


def canonical_asset_root(argument):
    absolute = os.path.abspath(argument)
    real = os.path.realpath(absolute)
    if os.path.normcase(real) != os.path.normcase(absolute):
        reject("asset root path must not contain symbolic links")
    metadata = os.lstat(absolute)
    if not stat.S_ISDIR(metadata.st_mode) or stat.S_ISLNK(metadata.st_mode):
        reject("asset root must be a non-symlink directory")
    actual_entries = set(os.listdir(absolute))
    if actual_entries != set(EXPECTED_OUTER_ASSETS):
        missing = sorted(set(EXPECTED_OUTER_ASSETS) - actual_entries)
        extra = sorted(actual_entries - set(EXPECTED_OUTER_ASSETS))
        reject("asset root entries mismatch; missing={!r}, extra={!r}".format(missing, extra))
    return absolute


def validate_assets(asset_root, source_commit, source_tag_object):
    if LOWER_OBJECT_ID.fullmatch(source_commit) is None:
        reject("expected source commit must be a lowercase 40-character object ID")
    if LOWER_OBJECT_ID.fullmatch(source_tag_object) is None:
        reject("expected source tag object must be a lowercase 40-character object ID")
    if source_commit == source_tag_object:
        reject("source commit and annotated tag object must be distinct")

    root = canonical_asset_root(asset_root)

    checksum_file, checksum_before = open_verified_asset(root, "SHA256SUMS")
    try:
        checksum_data = checksum_file.read(MAX_MANIFEST_BYTES + 1)
        if checksum_data != expected_outer_manifest():
            reject("outer SHA256SUMS differs from the exact reviewed contract")
    finally:
        close_verified_asset(checksum_file, checksum_before, "SHA256SUMS")

    summaries = {}
    for architecture in ("amd64", "arm64"):
        name = "probe-panel-management-v1.2.0-linux-{}.tar.gz".format(architecture)
        archive, archive_before = open_verified_asset(root, name)
        try:
            summaries[architecture] = validate_archive(
                archive, architecture, source_commit, source_tag_object
            )
        finally:
            close_verified_asset(archive, archive_before, name)
    return summaries


def main(arguments):
    if len(arguments) != 4:
        sys.stderr.write(
            "usage: {} ASSET_ROOT EXPECTED_SOURCE_COMMIT "
            "EXPECTED_SOURCE_TAG_OBJECT\n".format(arguments[0])
        )
        return 2
    try:
        summaries = validate_assets(arguments[1], arguments[2], arguments[3])
    except (ValidationError, OSError, UnicodeError) as error:
        sys.stderr.write("probe release tar validation failed: {}\n".format(error))
        return 1
    for architecture in ("amd64", "arm64"):
        summary = summaries[architecture]
        print(
            "linux-{}: PASS members={} files={} directories={} "
            "payload_files={} expanded_file_bytes={}".format(
                architecture,
                summary["members"],
                summary["files"],
                summary["directories"],
                summary["payload_files"],
                summary["expanded_file_bytes"],
            )
        )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
