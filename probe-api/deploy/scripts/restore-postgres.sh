#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

readonly BACKUP_MARKER_VALUE='probe-postgres-backup-v1'
readonly DEFAULT_BACKUP_ROOT='/var/backups/probe-panel/postgres'

die() {
  printf 'probe-postgres-restore: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'USAGE'
Usage:
  restore-postgres.sh --confirm-database DATABASE /absolute/path/to/probe-TIMESTAMP.dump

The confirmation value must exactly match SELECT current_database(). The archive
must be a verified daily or weekly backup below PROBE_POSTGRES_BACKUP_DIR.
USAGE
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
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
  export PGAPPNAME='probe-postgres-restore'

  if [[ -n ${PGPASSFILE:-} ]]; then
    [[ $PGPASSFILE == /* && -f $PGPASSFILE && ! -L $PGPASSFILE ]] || die 'PGPASSFILE must be an absolute regular file, not a symlink'
    [[ -O $PGPASSFILE ]] || die 'PGPASSFILE must be owned by the restore service user'
    local mode
    mode=$(stat -c '%a' -- "$PGPASSFILE")
    (( (8#$mode & 077) == 0 )) || die 'PGPASSFILE must not grant group or other permissions'
  fi
}

confirmed_database=''
archive_argument=''

while (( $# > 0 )); do
  case "$1" in
    --confirm-database)
      (( $# >= 2 )) || die '--confirm-database requires a value'
      [[ -z $confirmed_database ]] || die '--confirm-database may be specified only once'
      confirmed_database=$2
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    --)
      shift
      (( $# == 1 )) || die 'exactly one backup archive is required'
      archive_argument=$1
      shift
      ;;
    -*)
      die "unknown option: $1"
      ;;
    *)
      [[ -z $archive_argument ]] || die 'exactly one backup archive is required'
      archive_argument=$1
      shift
      ;;
  esac
done

[[ -n $confirmed_database ]] || die '--confirm-database is required'
[[ $confirmed_database != *$'\n'* && $confirmed_database != *$'\r'* ]] || die 'database confirmation contains a control character'
[[ -n $archive_argument && $archive_argument == /* ]] || die 'backup archive must be an absolute path'
[[ $archive_argument != *$'\n'* && $archive_argument != *$'\r'* ]] || die 'backup archive path contains a control character'

for command_name in psql pg_restore sha256sum realpath flock find stat dirname basename wc; do
  require_command "$command_name"
done

configured_root=${PROBE_POSTGRES_BACKUP_DIR:-$DEFAULT_BACKUP_ROOT}
[[ -n $configured_root && $configured_root == /* ]] || die 'PROBE_POSTGRES_BACKUP_DIR must be an absolute path'
[[ $configured_root != *$'\n'* && $configured_root != *$'\r'* ]] || die 'backup root contains a control character'
[[ ! -L $configured_root ]] || die 'backup root must not be a symbolic link'
backup_root=$(realpath -e -- "$configured_root")
case "$backup_root" in
  /|/etc|/home|/media|/mnt|/opt|/root|/run|/srv|/tmp|/usr|/var|/var/backups)
    die "backup root is too broad: $backup_root"
    ;;
esac
[[ -d $backup_root && ! -L $backup_root ]] || die 'backup root must be a real directory'
[[ -O $backup_root ]] || die "backup root must be owned by uid $(id -u): $backup_root"
root_mode=$(stat -c '%a' -- "$backup_root")
(( (8#$root_mode & 077) == 0 )) || die 'backup root must not grant group or other permissions'

marker="$backup_root/.probe-postgres-backup-root"
[[ -f $marker && ! -L $marker ]] || die 'backup root marker is missing or unsafe'
[[ $(<"$marker") == "$BACKUP_MARKER_VALUE" ]] || die 'backup root marker has an unexpected value'

exec 9<"$backup_root"
flock -n 9 || die 'another backup or restore operation is running'

[[ ! -L $archive_argument ]] || die 'backup archive must not be a symbolic link'
archive_path=$(realpath -e -- "$archive_argument")
[[ -f $archive_path && ! -L $archive_path ]] || die 'backup archive is not a regular file'
archive_directory=$(dirname -- "$archive_path")
archive_name=$(basename -- "$archive_path")
[[ $archive_name =~ ^probe-[0-9]{8}T[0-9]{6}Z\.dump$ ]] || die 'backup archive name does not match the managed format'
case "$archive_directory" in
  "$backup_root/daily"|"$backup_root/weekly") ;;
  *) die 'backup archive is outside the managed daily and weekly directories' ;;
esac
[[ $(realpath -e -- "$archive_directory") == "$archive_directory" ]] || die 'backup archive directory escaped its root'
[[ -O $archive_directory ]] || die 'backup archive directory must be owned by the restore service user'
directory_mode=$(stat -c '%a' -- "$archive_directory")
(( (8#$directory_mode & 077) == 0 )) || die 'backup archive directory must not grant group or other permissions'
[[ -O $archive_path ]] || die 'backup archive must be owned by the restore service user'

checksum_path="$archive_path.sha256"
[[ -f $checksum_path && ! -L $checksum_path ]] || die 'matching checksum file is missing or unsafe'
[[ -O $checksum_path ]] || die 'checksum file must be owned by the restore service user'
for protected_file in "$archive_path" "$checksum_path"; do
  mode=$(stat -c '%a' -- "$protected_file")
  (( (8#$mode & 077) == 0 )) || die "backup material must not grant group or other permissions: $protected_file"
done

[[ $(wc -l <"$checksum_path") -eq 1 ]] || die 'checksum file must contain exactly one entry'
read -r expected_hash checksum_name extra <"$checksum_path"
checksum_name=${checksum_name#\*}
[[ $expected_hash =~ ^[[:xdigit:]]{64}$ && $checksum_name == "$archive_name" && -z ${extra:-} ]] || die 'checksum entry does not name exactly the selected archive'
(cd -- "$archive_directory" && sha256sum --check --strict --status "$archive_name.sha256") || die 'backup checksum verification failed'
pg_restore --list "$archive_path" >/dev/null || die 'pg_restore could not read the selected archive'

configure_database_environment

current_database=$(psql -X --no-password --tuples-only --no-align --set=ON_ERROR_STOP=1 --command='SELECT current_database()')
current_database=${current_database//$'\r'/}
current_database=${current_database//$'\n'/}
[[ -n $current_database ]] || die 'could not determine the target database'
case "$current_database" in
  postgres|template0|template1) die "refusing to restore into PostgreSQL system database: $current_database" ;;
esac
[[ $confirmed_database == "$current_database" ]] || die "confirmation does not match target database '$current_database'"

active_sessions=$(psql -X --no-password --tuples-only --no-align --set=ON_ERROR_STOP=1 \
  --command='SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()')
active_sessions=${active_sessions//[[:space:]]/}
[[ $active_sessions =~ ^[0-9]+$ ]] || die 'could not verify active database sessions'
(( active_sessions == 0 )) || die "target database still has $active_sessions other session(s); stop probe-api and all clients first"

printf 'Restoring verified archive %s into database %s...\n' "$archive_path" "$current_database"
pg_restore \
  --clean \
  --if-exists \
  --exit-on-error \
  --no-owner \
  --no-privileges \
  --file=- \
  "$archive_path" \
  | psql \
      -X \
      --no-password \
      --quiet \
      --single-transaction \
      --set=ON_ERROR_STOP=1

psql -X --no-password --tuples-only --no-align --set=ON_ERROR_STOP=1 --command='SELECT 1' >/dev/null
printf 'PostgreSQL restore completed: database=%s archive=%s\n' "$current_database" "$archive_path"
