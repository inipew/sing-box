#!/usr/bin/env python3
"""Send build artifacts to a Telegram channel as a document album."""

from __future__ import annotations

import asyncio
import logging
import sys
from itertools import islice
from pathlib import Path

from hydrogram import Client, enums
from hydrogram.errors import FloodWait
from hydrogram.types import InputMediaDocument
from pydantic_settings import BaseSettings, SettingsConfigDict
from rich.console import Console
from rich.logging import RichHandler
from rich.progress import BarColumn, MofNCompleteColumn, Progress, SpinnerColumn, TextColumn
from tenacity import RetryCallState, retry, stop_after_attempt

# ─── LOGGING ──────────────────────────────────────────────────────────────────

logging.basicConfig(
    level=logging.INFO,
    format="%(message)s",
    datefmt="[%X]",
    handlers=[RichHandler(rich_tracebacks=True, markup=True)],
)
log = logging.getLogger(__name__)
console = Console()

ALBUM_LIMIT = 10
MAX_FILE_SIZE = 2 * 1024 * 1024 * 1024  # 2 GB


# ─── CONFIG ───────────────────────────────────────────────────────────────────

class Config(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    api_id: int
    api_hash: str
    bot_token: str
    chat_id: int
    version: str = ""
    commit: str = ""
    cherry_pick_commit: str = ""
    tags: str = ""


# ─── CAPTION BUILDER ──────────────────────────────────────────────────────────

def _commit_lines(raw: str) -> str:
    lines: list[str] = []
    for line in raw.strip().splitlines():
        line = line.strip()
        for sep in (" ", "—"):
            if sep in line:
                sha, _, msg = line.partition(sep)
                break
        else:
            sha, msg = line, ""
        sha = sha.replace("`", "").strip()
        lines.append(f"<code>{sha}</code> — {msg.strip(' —')}")
    return "\n".join(lines)


def build_caption(cfg: Config) -> str:
    parts: list[str] = []
    if cfg.version:
        parts.append(f"🚀 <b>Sing-box {cfg.version}</b>")
    if cfg.tags:
        parts.append(f"🏷 <b>Tags:</b>\n<code>{cfg.tags}</code>")
    if cfg.commit:
        parts.append(f"🔨 <b>Commit:</b>\n{_commit_lines(cfg.commit)}")
    if cfg.cherry_pick_commit:
        parts.append(f"🔨 <b>Cherry-pick:</b>\n{_commit_lines(cfg.cherry_pick_commit)}")
    return "\n\n".join(parts)


# ─── RETRY WAIT ───────────────────────────────────────────────────────────────

class FloodAwareWait:
    """Respect Telegram's FloodWait; fall back to capped exponential back-off."""

    def __call__(self, retry_state: RetryCallState) -> float:
        exc = retry_state.outcome.exception()
        if isinstance(exc, FloodWait):
            log.warning("Flood wait: sleeping %ds as requested by Telegram", exc.value)
            return float(exc.value)
        delay = min(2.0 * (2 ** (retry_state.attempt_number - 1)), 30.0)
        log.debug("Retrying in %.1fs (attempt %d)", delay, retry_state.attempt_number)
        return delay


# ─── HELPERS ──────────────────────────────────────────────────────────────────

def _batched(files: list[Path], n: int):
    """Yield successive n-sized chunks from a list."""
    it = iter(files)
    while chunk := list(islice(it, n)):
        yield chunk


# ─── UPLOADER ─────────────────────────────────────────────────────────────────

class TelegramUploader:
    __slots__ = ("cfg",)

    def __init__(self, cfg: Config) -> None:
        self.cfg = cfg

    @retry(stop=stop_after_attempt(5), wait=FloodAwareWait(), reraise=True)
    async def _send_album(self, app: Client, media: list[InputMediaDocument]) -> list:
        return await app.send_media_group(chat_id=self.cfg.chat_id, media=media)

    def _validate(self, files: list[Path]) -> list[Path]:
        valid: list[Path] = []
        for f in files:
            if not f.exists():
                log.error("Not found: %s", f)
            elif f.stat().st_size > MAX_FILE_SIZE:
                log.error("Too large (>2 GB): %s", f.name)
            else:
                valid.append(f)
        return valid

    async def upload(self, files: list[Path]) -> None:
        valid = self._validate(files)
        if not valid:
            log.error("No valid files to upload")
            sys.exit(1)

        caption = build_caption(self.cfg)
        chunks = list(_batched(valid, ALBUM_LIMIT))
        total_chunks = len(chunks)
        pinned: int | None = None

        # Caption on the very last item so Telegram renders all files first, then caption below
        last_chunk_idx = total_chunks - 1
        last_file_idx = len(chunks[-1]) - 1

        async with Client(
            ":memory:",
            api_id=self.cfg.api_id,
            api_hash=self.cfg.api_hash,
            bot_token=self.cfg.bot_token,
        ) as app:
            with Progress(
                SpinnerColumn(),
                TextColumn("[progress.description]{task.description}"),
                BarColumn(),
                MofNCompleteColumn(),
                console=console,
                transient=True,
            ) as progress:
                task = progress.add_task("Uploading…", total=total_chunks)

                for chunk_idx, chunk in enumerate(chunks):
                    progress.update(
                        task,
                        description=(
                            f"Chunk {chunk_idx + 1}/{total_chunks} "
                            f"({len(chunk)} file(s))"
                        ),
                    )
                    media = [
                        InputMediaDocument(
                            media=str(path),
                            **(
                                {"caption": caption, "parse_mode": enums.ParseMode.HTML}
                                if chunk_idx == last_chunk_idx and file_idx == last_file_idx
                                else {}
                            ),
                        )
                        for file_idx, path in enumerate(chunk)
                    ]

                    messages = await self._send_album(app, media)
                    progress.advance(task)

                    if pinned is None:
                        pinned = messages[0].id
                        try:
                            await app.pin_chat_message(
                                chat_id=self.cfg.chat_id,
                                message_id=pinned,
                                disable_notification=True,
                            )
                        except Exception as exc:
                            log.debug("Pin failed (non-fatal): %s", exc)

        log.info("✓ Uploaded %d file(s) in %d chunk(s)", len(valid), total_chunks)


# ─── MAIN ─────────────────────────────────────────────────────────────────────

async def main() -> None:
    if len(sys.argv) < 2:
        log.error("Usage: sendtotelegramchannel.py <file> [file …]")
        sys.exit(1)

    cfg = Config()
    await TelegramUploader(cfg).upload([Path(a) for a in sys.argv[1:]])


if __name__ == "__main__":
    asyncio.run(main())
