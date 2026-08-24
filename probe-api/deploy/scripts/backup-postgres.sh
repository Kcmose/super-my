#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

readonly BACKUP_MARKER_VALUE='probe-postgres-backup-v1'
readonly DEFAULT_BACKUP_ROOT='/var/backups/probe-panel/postgres'

backup_root=''
daily_dir=''
weekly_dir=''
temp_dump=''
checksum_temp=''
weekly_temp=''
marker_temp=''
daily_output=''
weekly_output=''
daily_created=0
weekly_created=0
daily_committed=0
weekly_committed=0

die() {
  printf 'probe-postgres-backup: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

require_bounded_integer() {
  local name=$1
  local value=$2
  local maximum=$3
  [[ $value =~ ^[0-9]+$ ]] || die "$name must be an integer"
  (( value >= 1 && value <= maximum )) || die "$name must be between 1 and $maximum"
}

cleanup() {
  local status=$?
  trap - EXIT
  for temporary in "$temp_dump" "$checksum_temp" "$weekly_temp" "$marker_temp"; do
    if [[ -n $temporary && -f $temporary && ! -L $temporary ]]; then
      rm -f -- "$temporary"
    fi
  done
  if (( status != 0 && daily_created == 1 && daily_committed == 0 )) && [[ -n $daily_output ]]; then
    rm -f -- "$daily_output" "$daily_output.sha256"
  fi
  if (( status != 0 && weekly_created == 1 && weekly_committed == 0 )) && [[ -n $weekly_output ]]; then
    rm -f -- "$weekly_output" "$weekly_output.sha256"
  fi
  exit "$status"
}
trap cleanup EXIT

validate_backup_root() {
  local configured=${PROBE_POSTGRES_BACKUP_DIR:-$DEFAULT_BACKUP_ROOT}
  local normalized
  local unexpected
  local marker

  [[ -n $configured && $configured == /* ]] || die 'PROBE_POSTGRES_BACKUP_DIR must be an absolute path'
  [[ $configured != *$'\n'* && $configured != *$'\r'* ]] || die 'backup path contains a control character'
  [[ ! -L $configured ]] || die 'backup root must not be a symbolic link'

  normalized=$(realpath -m -- "$configured")
  case "$normalized" in
    /|/etc|/home|/media|/mnt|/opt|/root|/run|/srv|/tmp|/usr|/var|/var/backups)
      die "backup root is too broad: $normalized"
      ;;
  esac

  mkdir -p -- "$normalized"
  backup_root=$(realpath -e -- "$normalized")
  [[ -d $backup_root && ! -L $backup_root ]] || die 'backup root must be a real directory'
  [[ -O $backup_root ]] || die "backup root must be owned by uid $(id -u): $backup_root"
  chmod 0700 -- "$backup_root"

  exec 9<"$backup_root"
  flock -n 9 || die 'another backup or restore operation is running'

  marker="$backup_root/.probe-postgres-backup-root"
  if [[ -e $marker ]]; then
    [[ -f $marker && ! -L $marker ]] || die 'backup root marker is not a regular file'
    [[ $(<"$marker") == "$BACKUP_MARKER_VALUE" ]] || die 'backup root marker has an unexpected value'
  else
    unexpected=$(find "$backup_root" -mindepth 1 -maxdepth 1 -print -quit)
    [[ -z $unexpected ]] || die 'refusing to initialize a non-empty backup root without its marker'
    marker_temp=$(mktemp "$backup_root/.probe-marker.XXXXXX")
    printf '%s\n' "$BACKUP_MARKER_VALUE" >"$marker_temp"
    chmod 0600 -- "$marker_temp"
    mv -- "$marker_temp" "$backup_root/.probe-postgres-backup-root"
    marker_temp=''
  fi

  daily_dir="$backup_root/daily"
  weekly_dir="$backup_root/weekly"
  for directory in "$daily_dir" "$weekly_dir"; do
    [[ ! -L $directory ]] || die "backup subdirectory must not be a symbolic link: $directory"
    mkdir -p -- "$directory"
    [[ $(realpath -e -- "$directory") == "$directory" ]] || die "backup subdirectory escaped its root: $directory"
    [[ -O $directory ]] || die "backup subdirectory is not owned by uid $(id -u): $directory"
    chmod 0700 -- "$directory"
  done
}

configure_database_environment() {
  local variable value
  for variable in PGHOST PGPORT PGDATABASE PGUSER; do
    value=${!variable:-}
    [[ -n $value ]] || die "set the discrete libpq variable $variable"
    [[ $value != *$'\n'* && $value != *$'\r'* ]] || die "$variable contains a control character"
  done
  if [[ ! $PGPORT =~ ^[0-9]+$ ]] || (( PGPORT < 1 || PGPORT > 65535 )); then
    die 'PGPORT must be between 1 and 65535'
  fi
  case "$PGDATABASE" in
    postgres://*|postgresql://*|*=*)
      die 'PGDATABASE must be a database name, not a URL or connection string; use discrete PG* variables'
      ;;
  esac
  export PGAPPNAME='probe-postgres-backup'

  if [[ -n ${PGPASSFILE:-} ]]; then
    [[ $PGPASSFILE == /* && -f $PGPASSFILE && ! -L $PGPASSFILE ]] || die 'PGPASSFILE must be an absolute regular file, not a symlink'
    [[ -O $PGPASSFILE ]] || die 'PGPASSFILE must be owned by the backup service user'
    local mode
    mode=$(stat -c '%a' -- "$PGPASSFILE")
    (( (8#$mode & 077) == 0 )) || die 'PGPASSFILE must not grant group or other permissions'
  fi
}

write_checksum() {
  local directory=$1
  local archive_name=$2
  checksum_temp=$(mktemp "$directory/.probe-checksum.XXXXXX")
  (cd -- "$directory" && sha256sum -- "$archive_name") >"$checksum_temp"
  chmod 0600 -- "$checksum_temp"
  mv -- "$checksum_temp" "$directory/$archive_name.sha256"
  checksum_temp=''
  (cd -- "$directory" && sha256sum --check --strict --status "$archive_name.sha256") || die "checksum verification failed: $directory/$archive_name"
}

rotate_archives() {
  local directory=$1
  local keep=$2
  local -a archives=()
  local index
  local archive_name
  local archive_path

  mapfile -t archives < <(find "$directory" -mindepth 1 -maxdepth 1 -type f -name 'probe-*.dump' -printf '%f\n' | LC_ALL=C sort -r)
  for (( index=keep; index<${#archives[@]}; index++ )); do
    archive_name=${archives[$index]}
    [[ $archive_name =~ ^probe-[0-9]{8}T[0-9]{6}Z\.dump$ ]] || die "unexpected archive name in rotation set: $archive_name"
    archive_path="$directory/$archive_name"
    [[ -f $archive_path && ! -L $archive_path ]] || die "refusing to rotate a non-regular archive: $archive_path"
    [[ $(realpath -e -- "$archive_path") == "$archive_path" ]] || die "archive escaped its retention directory: $archive_path"
    rm -f -- "$archive_path" "$archive_path.sha256"
  done
}

for command_name in pg_dump pg_restore sha256sum realpath flock find sort date mktemp stat; do
  require_command "$command_name"
done

daily_keep=${PROBE_POSTGRES_DAILY_KEEP:-7}
weekly_keep=${PROBE_POSTGRES_WEEKLY_KEEP:-4}
weekly_day=${PROBE_POSTGRES_WEEKLY_DAY:-7}
require_bounded_integer PROBE_POSTGRES_DAILY_KEEP "$daily_keep" 365
require_bounded_integer PROBE_POSTGRES_WEEKLY_KEEP "$weekly_keep" 260
require_bounded_integer PROBE_POSTGRES_WEEKLY_DAY "$weekly_day" 7

validate_backup_root
configure_database_environment

timestamp=$(date -u '+%Y%m%dT%H%M%SZ')
archive_name="probe-$timestamp.dump"
daily_output="$daily_dir/$archive_name"
[[ ! -e $daily_output && ! -e $daily_output.sha256 ]] || die "backup archive already exists: $daily_output"

temp_dump=$(mktemp "$daily_dir/.probe-backup.XXXXXX.dump")
pg_dump \
  --format=custom \
  --compress=6 \
  --no-owner \
  --no-privileges \
  --no-password \
  --file="$temp_dump"
pg_restore --list "$temp_dump" >/dev/null || die 'pg_restore could not read the new archive'
chmod 0600 -- "$temp_dump"
mv -- "$temp_dump" "$daily_output"
temp_dump=''
daily_created=1
write_checksum "$daily_dir" "$archive_name"
daily_committed=1

if [[ $(date '+%u') == "$weekly_day" ]]; then
  weekly_output="$weekly_dir/$archive_name"
  [[ ! -e $weekly_output && ! -e $weekly_output.sha256 ]] || die "weekly archive already exists: $weekly_output"
  weekly_temp=$(mktemp "$weekly_dir/.probe-weekly.XXXXXX.dump")
  cp --reflink=auto -- "$daily_output" "$weekly_temp"
  chmod 0600 -- "$weekly_temp"
  pg_restore --list "$weekly_temp" >/dev/null || die 'pg_restore could not read the weekly archive copy'
  mv -- "$weekly_temp" "$weekly_output"
  weekly_temp=''
  weekly_created=1
  write_checksum "$weekly_dir" "$archive_name"
  weekly_committed=1
fi

rotate_archives "$daily_dir" "$daily_keep"
rotate_archives "$weekly_dir" "$weekly_keep"

printf 'PostgreSQL backup verified: %s\n' "$daily_output"
if (( weekly_committed == 1 )); then
  printf 'Weekly backup verified: %s\n' "$weekly_output"
fi
