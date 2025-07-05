#!/usr/bin/env python3
import asyncio
import sys
from pathlib import Path
from typing import List

from aiogram import Bot
from aiogram.types import FSInputFile, InputMediaDocument
from aiogram.exceptions import TelegramRetryAfter
from pydantic_settings import BaseSettings, SettingsConfigDict
from pydantic import Field
from tenacity import retry, stop_after_attempt, wait_exponential
from rich.console import Console

console = Console()

ALBUM_LIMIT = 10
MAX_FILE_SIZE = 2 * 1024 * 1024 * 1024  # 2GB


# =============================
# CONFIG (Pydantic v2 CLEAN)
# =============================

class Config(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore"
    )

    bot_token: str
    chat_id: int
    version: str = ""
    commit: str = ""
    cherry_pick_commit: str = ""
    tags: str = ""


# =============================
# CAPTION BUILDER
# =============================

def format_commit_block(title: str, content: str) -> str:
    if not content.strip():
        return ""

    lines = []
    for line in content.strip().splitlines():
        line = line.strip()

        # split SHA dan pesan commit
        if " " in line:
            sha, message = line.split(" ", 1)
        elif "—" in line:
            sha, message = line.split("—", 1)
        else:
            sha = line
            message = ""

        sha = sha.replace("`", "").strip()
        message = message.strip(" —")

        lines.append(f"<code>{sha}</code> — {message}")

    return f"🔨 <b>{title}:</b>\n" + "\n".join(lines) + "\n"


def build_caption(cfg: Config) -> str:
    parts = []

    if cfg.version:
        parts.append(f"🚀 <b>Sing-box {cfg.version}</b>\n")

    if cfg.tags:
        parts.append(
            "🏷 <b>Tags:</b>\n"
            f"<code>{cfg.tags}</code>\n"
        )

    if cfg.commit:
        parts.append(format_commit_block("Commit", cfg.commit))

    if cfg.cherry_pick_commit:
        parts.append(format_commit_block("Cherry-pick", cfg.cherry_pick_commit))

    return "\n".join(parts).strip()


# =============================
# TELEGRAM UPLOADER
# =============================

class TelegramUploader:
    def __init__(self, cfg: Config):
        self.cfg = cfg
        self.bot = Bot(token=cfg.bot_token)

    @retry(
        stop=stop_after_attempt(5),
        wait=wait_exponential(multiplier=1, min=2, max=30),
        reraise=True
    )
    async def _send_album(self, media):
        return await self.bot.send_media_group(
            chat_id=self.cfg.chat_id,
            media=media
        )

    async def upload(self, files: List[Path]):
        valid_files = []

        for f in files:
            if not f.exists():
                console.print(f"[red]File not found:[/red] {f}")
                continue

            if f.stat().st_size > MAX_FILE_SIZE:
                console.print(f"[red]Too large (>2GB):[/red] {f.name}")
                continue

            valid_files.append(f)

        if not valid_files:
            console.print("[red]No valid files[/red]")
            return

        caption = build_caption(self.cfg)

        # Split per 10 files
        for i in range(0, len(valid_files), ALBUM_LIMIT):
            chunk = valid_files[i:i + ALBUM_LIMIT]
            media = []

            for idx, file_path in enumerate(chunk):
                file = FSInputFile(file_path)

                # Caption di FILE TERAKHIR
                if idx == len(chunk) - 1:
                    media.append(
                        InputMediaDocument(
                            media=file,
                            caption=caption,
                            parse_mode="HTML"
                        )
                    )
                else:
                    media.append(InputMediaDocument(media=file))

            try:
                console.print("[yellow]Uploading album...[/yellow]")
                messages = await self._send_album(media)

                console.print("[green]Upload success[/green]")

                # Pin first album only
                if i == 0:
                    try:
                        await self.bot.pin_chat_message(
                            chat_id=self.cfg.chat_id,
                            message_id=messages[0].message_id,
                            disable_notification=True
                        )
                    except Exception:
                        pass

            except TelegramRetryAfter as e:
                console.print(f"[red]Flood wait {e.retry_after}s[/red]")
                await asyncio.sleep(e.retry_after)
                await self.upload(chunk)

            except Exception as e:
                console.print(f"[red]Upload failed:[/red] {e}")

    async def close(self):
        await self.bot.session.close()


# =============================
# MAIN
# =============================

async def main():
    if len(sys.argv) < 2:
        console.print("[red]No files provided[/red]")
        sys.exit(1)

    cfg = Config()
    uploader = TelegramUploader(cfg)

    files = [Path(f) for f in sys.argv[1:]]

    await uploader.upload(files)
    await uploader.close()


if __name__ == "__main__":
    asyncio.run(main())