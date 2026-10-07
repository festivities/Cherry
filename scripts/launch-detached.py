"""Start Cherry without inheriting the invoking harness's console handles."""
import subprocess
import sys
from pathlib import Path

exe = Path(sys.argv[1]).resolve(strict=True)
log = Path(sys.argv[2]).resolve()
workdir = Path(sys.argv[3]).resolve(strict=True)
if not log.parent.is_dir():
    raise SystemExit("log parent directory is missing")

with log.open("ab", buffering=0) as output:
    process = subprocess.Popen(
        [str(exe)],
        cwd=str(workdir),
        stdin=subprocess.DEVNULL,
        stdout=output,
        stderr=subprocess.STDOUT,
        close_fds=True,
        creationflags=subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP,
    )
print(process.pid, flush=True)
