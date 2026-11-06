#!/usr/bin/env python3
"""Stage one pinned CPA archive. No download, shell, extractall or execution."""
import gzip
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import sys
import tarfile
import time

ASSET = "CLIProxyAPI_8.0.55_linux_amd64.tar.gz"
SHA256 = "4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123"
SIZE = 25229124
MEMBERS = {"cli-proxy-api", "LICENSE", "README.md", "README_CN.md", "config.example.yaml"}
CHUNK = 65536
LIMIT = 512 * 1024 * 1024


class Rejected(Exception):
    pass


def require(value):
    if not value:
        raise Rejected()


def identity(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_size,
            info.st_mtime_ns, info.st_ctime_ns, info.st_nlink)


def owned(path, directory=False):
    require(path.is_absolute() and path.resolve() == path)
    info = path.lstat()
    require((stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode))
            and not info.st_mode & 0o022
            and (not hasattr(os, "getuid") or info.st_uid == os.getuid()))
    if not directory:
        require(info.st_nlink == 1)
    return info


class BoundedReader:
    def __init__(self, source, deadline, limit=LIMIT):
        self.source, self.deadline, self.remaining = source, deadline, limit

    def read(self, size):
        require(0 <= size <= CHUNK and time.monotonic() < self.deadline)
        data = self.source.read(min(size, self.remaining + 1))
        self.remaining -= len(data)
        require(self.remaining >= 0)
        return data


class BoundedTarInfo(tarfile.TarInfo):
    def _proc_member(self, archive):
        # Enforce this BEFORE tarfile reads or decodes extension metadata.
        require(self.type in (tarfile.REGTYPE, tarfile.AREGTYPE,
                              tarfile.XHDTYPE, tarfile.XGLTYPE))
        if self.type in (tarfile.XHDTYPE, tarfile.XGLTYPE):
            require(self.size <= CHUNK)
        return super()._proc_member(archive)


def stage(archive, destination, *, expected_sha=SHA256, expected_size=SIZE):
    """Expected values are test injection only; the CLI always uses constants."""
    archive, destination = Path(archive), Path(destination)
    require(archive.name == ASSET and 0 < expected_size <= 128 * 1024 * 1024)
    before = owned(archive)
    require(before.st_size == expected_size)
    owned(destination.parent, directory=True)
    require(not destination.exists() and not destination.is_symlink())
    deadline = time.monotonic() + 120
    created = False
    try:
        fd = os.open(archive, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(fd, "rb") as source:
            require(identity(before) == identity(os.fstat(source.fileno())))
            hasher = hashlib.sha256()
            remaining = expected_size
            while remaining:
                require(time.monotonic() < deadline)
                data = source.read(min(CHUNK, remaining))
                require(data)
                hasher.update(data)
                remaining -= len(data)
            require(not source.read(1) and hasher.hexdigest() == expected_sha)
            require(identity(before) == identity(os.fstat(source.fileno())))
            source.seek(0)
            destination.mkdir(mode=0o700)
            created = True
            seen, binary_sha = set(), None
            with gzip.GzipFile(fileobj=source) as compressed:
                bounded = BoundedReader(compressed, deadline)
                with tarfile.open(fileobj=bounded, mode="r|", tarinfo=BoundedTarInfo) as bundle:
                    for member in bundle:
                        require(member.name in MEMBERS and member.name not in seen
                                and member.isreg() and member.sparse is None
                                and not member.mode & 0o7000 and 0 <= member.size <= LIMIT)
                        seen.add(member.name)
                        entry = bundle.extractfile(member)
                        require(entry is not None)
                        digest = hashlib.sha256()
                        prefix = b""
                        remaining = member.size
                        with entry, (destination / member.name).open("xb") as output:
                            os.chmod(output.name, 0o600)
                            while remaining:
                                data = entry.read(min(CHUNK, remaining))
                                require(data and time.monotonic() < deadline)
                                if len(prefix) < 64:
                                    prefix += data[:64 - len(prefix)]
                                output.write(data)
                                digest.update(data)
                                remaining -= len(data)
                        if member.name == "cli-proxy-api":
                            require(len(prefix) >= 64 and prefix[:7] == b"\x7fELF\x02\x01\x01"
                                    and prefix[18:20] == b"\x3e\x00")
                            binary_sha = digest.hexdigest()
                # Consume the gzip trailer too. Corrupt CRC / truncated streams
                # cannot become accepted merely because tar reached its EOF.
                while True:
                    tail = bounded.read(CHUNK)
                    if not tail:
                        break
                    require(not any(tail))
            require(seen == MEMBERS and binary_sha is not None)
            require(identity(before) == identity(os.fstat(source.fileno()))
                    == identity(owned(archive)))
        for name in seen:
            os.chmod(destination / name, 0o500 if name == "cli-proxy-api" else 0o400)
        return {"observedArchiveSha256": expected_sha, "observedBinarySha256": binary_sha}
    except BaseException:
        if created:
            shutil.rmtree(destination)
        raise


def main(argv):
    if len(argv) != 2:
        print('{"errorCode":"USAGE"}')
        return 2
    try:
        require(sys.version_info >= (3, 11))
        print(json.dumps(stage(*argv), separators=(",", ":")))
        return 0
    except (Exception, KeyboardInterrupt):
        print('{"errorCode":"ARTIFACT_INVALID"}')
        return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
