import asyncio
import logging
import os
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import List, Optional, Final
from functools import lru_cache

from pyrogram import Client, errors, enums
from pyrogram.types import InputMediaDocument, Message
from rich.console import Console
from rich.logging import RichHandler
from rich.progress import Progress, SpinnerColumn, TextColumn, TimeElapsedColumn

# Constants
MAX_CAPTION_LENGTH: Final[int] = 1024
MAX_RETRY_ATTEMPTS: Final[int] = 3
RETRY_DELAY: Final[int] = 5

@dataclass(frozen=True)
class Config:
    """Configuration class with immutable attributes."""
    api_id: str
    api_hash: str
    chat_id: int
    bot_token: str
    version: str
    commit: str = ""
    cherry_pick_commit: str = ""
    tags: str = field(default="")

    @classmethod
    def from_env(cls) -> 'Config':
        """Create Config instance from environment variables with better error handling."""
        required_vars = {
            "API_ID": str,
            "API_HASH": str,
            "CHAT_ID": int,
            "BOT_TOKEN": str,
            "VERSION": str,
        }
        
        config_data = {}
        
        try:
            for var_name, var_type in required_vars.items():
                value = os.environ.get(var_name)
                if value is None:
                    raise ValueError(f"Missing required environment variable: {var_name}")
                
                try:
                    config_data[var_name.lower()] = var_type(value)
                except ValueError:
                    raise ValueError(f"Invalid value for {var_name}: {value}")
            
            # Optional variables
            config_data.update({
                "commit": os.environ.get("COMMIT", ""),
                "cherry_pick_commit": os.environ.get("CHERRY_PICK_COMMIT", ""),
                "tags": os.environ.get("TAGS", "")
            })
            
            return cls(**config_data)
        except Exception as e:
            raise ValueError(f"Configuration error: {str(e)}") from e

class MessageBuilder:
    """Handles message template building with caching."""
    
    TEMPLATE: Final[str] = """
Sing-box {version}

Tags: {tags}

{update_section}
Cherry-pick:
{cherry_pick_commit}

[SagerNet/sing-box](https://github.com/SagerNet/sing-box)
    """.strip()

    def __init__(self, config: Config):
        self.config = config

    @lru_cache(maxsize=1)
    def build(self) -> str:
        """Build message with caching for repeated calls."""
        update_section = f"Update:\n{self.config.commit}\n\n" if self.config.commit else ""
        
        msg = self.TEMPLATE.format(
            version=self.config.version,
            tags=self.config.tags,
            update_section=update_section,
            cherry_pick_commit=self.config.cherry_pick_commit
        )
        
        if len(msg) > MAX_CAPTION_LENGTH:
            msg = self.TEMPLATE.format(
                version=self.config.version,
                tags=self.config.tags,
                update_section="",
                cherry_pick_commit=self.config.cherry_pick_commit
            )
        
        return msg

class TelegramUploader:
    """Handles file uploads to Telegram with improved error handling and retries."""
    
    def __init__(self, config: Config):
        self.config = config
        self.console = Console()
        self.logger = self._setup_logger()
        
    @staticmethod
    def _setup_logger() -> logging.Logger:
        """Setup logger with detailed formatting."""
        logging_format = "%(asctime)s - %(name)s - %(levelname)s - %(message)s"
        logging.basicConfig(
            level=logging.INFO,
            format=logging_format,
            handlers=[RichHandler(rich_tracebacks=True, show_time=True)]
        )
        logger = logging.getLogger("telegram-uploader")
        logger.setLevel(logging.INFO)
        return logger

    async def _handle_upload_retry(self, attempt: int, error: Exception) -> None:
        """Handle upload retry logic."""
        if attempt < MAX_RETRY_ATTEMPTS:
            wait_time = RETRY_DELAY * (2 ** attempt)  # Exponential backoff
            self.logger.warning(f"Attempt {attempt + 1} failed. Retrying in {wait_time} seconds... Error: {error}")
            await asyncio.sleep(wait_time)
        else:
            raise Exception(f"Max retry attempts ({MAX_RETRY_ATTEMPTS}) reached. Last error: {error}")

    async def _pin_message(self, app: Client, message: Message) -> None:
        """Pin message with error handling."""
        try:
            await app.pin_chat_message(
                chat_id=self.config.chat_id,
                message_id=message.id,
                disable_notification=True
            )
        except Exception as e:
            self.logger.error(f"Failed to pin message: {e}")
            # Don't raise the error as this is not critical

    async def upload_files(self, files: List[Path]) -> None:
        """Upload files with improved error handling and progress tracking."""
        self.logger.info(f"Starting upload process for {len(files)} files")
        
        async with Client(
            "bot",
            api_id=self.config.api_id,
            api_hash=self.config.api_hash,
            bot_token=self.config.bot_token,
            in_memory=True
        ) as app:
            with Progress(
                SpinnerColumn(),
                TextColumn("[progress.description]{task.description}"),
                TimeElapsedColumn(),
                console=self.console
            ) as progress:
                upload_task = progress.add_task("Preparing files...", total=len(files))
                
                media = [
                    InputMediaDocument(
                        media=str(file),
                        caption="" if file != files[-1] else MessageBuilder(self.config).build(),
                        parse_mode=enums.ParseMode.MARKDOWN
                    )
                    for file in files
                ]
                
                for attempt in range(MAX_RETRY_ATTEMPTS):
                    try:
                        progress.update(upload_task, description=f"Uploading files (Attempt {attempt + 1})...")
                        sent_messages = await app.send_media_group(
                            chat_id=self.config.chat_id,
                            media=media
                        )
                        
                        if sent_messages:
                            await self._pin_message(app, sent_messages[-1])
                        
                        progress.update(upload_task, advance=len(files))
                        self.logger.info("Upload completed successfully")
                        break
                        
                    except errors.FloodWait as e:
                        self.logger.warning(f"Rate limit hit, waiting {e.value} seconds")
                        await asyncio.sleep(e.value)
                        continue
                        
                    except Exception as e:
                        await self._handle_upload_retry(attempt, e)

async def main() -> None:
    """Main function with improved error handling."""
    try:
        config = Config.from_env()
        
        files = [Path(f) for f in sys.argv[1:]]
        if not files:
            raise ValueError("No files specified for upload")
        
        # Validate all files before starting upload
        invalid_files = [f for f in files if not f.exists()]
        if invalid_files:
            raise FileNotFoundError(f"Files not found: {', '.join(str(f) for f in invalid_files)}")
        
        uploader = TelegramUploader(config)
        await uploader.upload_files(files)
        
    except Exception as e:
        logging.error(f"Fatal error: {str(e)}", exc_info=True)
        sys.exit(1)

if __name__ == "__main__":
    asyncio.run(main())