import asyncio
import logging
import os
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import List, Optional, Final, Dict, Any
from functools import lru_cache
import hashlib
import mimetypes

from pyrogram import Client, errors, enums
from pyrogram.types import InputMediaDocument, Message
from rich.console import Console
from rich.logging import RichHandler
from rich.progress import Progress, SpinnerColumn, TextColumn, TimeElapsedColumn, BarColumn

# Constants
MAX_CAPTION_LENGTH: Final[int] = 1024
MAX_RETRY_ATTEMPTS: Final[int] = 3
RETRY_DELAY: Final[int] = 5
MAX_FILE_SIZE: Final[int] = 50 * 1024 * 1024  # 50MB
MAX_MEDIA_GROUP_SIZE: Final[int] = 10
SUPPORTED_EXTENSIONS: Final[set] = {'.zip', '.tar.gz', '.7z', '.apk', '.exe', '.deb', '.rpm'}

@dataclass(frozen=True)
class Config:
    """Configuration class with immutable attributes."""
    api_id: int
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
            "API_ID": int,
            "API_HASH": str,
            "CHAT_ID": int,
            "BOT_TOKEN": str,
            "VERSION": str,
        }
        
        config_data = {}
        missing_vars = []
        
        try:
            for var_name, var_type in required_vars.items():
                value = os.environ.get(var_name)
                if value is None:
                    missing_vars.append(var_name)
                    continue
                
                try:
                    if var_type == int:
                        # Handle potential negative values for chat_id
                        config_data[var_name.lower()] = int(value)
                    else:
                        config_data[var_name.lower()] = var_type(value.strip())
                except ValueError as e:
                    raise ValueError(f"Invalid value for {var_name}: {value} (expected {var_type.__name__})") from e
            
            if missing_vars:
                raise ValueError(f"Missing required environment variables: {', '.join(missing_vars)}")
            
            # Optional variables with validation
            commit = os.environ.get("COMMIT", "").strip()
            cherry_pick_commit = os.environ.get("CHERRY_PICK_COMMIT", "").strip()
            tags = os.environ.get("TAGS", "").strip()
            
            config_data.update({
                "commit": commit,
                "cherry_pick_commit": cherry_pick_commit,
                "tags": tags
            })
            
            return cls(**config_data)
        except Exception as e:
            raise ValueError(f"Configuration error: {str(e)}") from e

    def validate(self) -> None:
        """Validate configuration values."""
        if not self.version.strip():
            raise ValueError("Version cannot be empty")
        
        if not self.bot_token.strip():
            raise ValueError("Bot token cannot be empty")
        
        if self.api_id <= 0:
            raise ValueError("API ID must be positive")

class FileValidator:
    """Validates files before upload with enhanced checks."""
    
    @staticmethod
    def get_file_hash(file_path: Path) -> str:
        """Calculate SHA256 hash of file."""
        hash_sha256 = hashlib.sha256()
        try:
            with open(file_path, "rb") as f:
                for chunk in iter(lambda: f.read(65536), b""):
                    hash_sha256.update(chunk)
            return hash_sha256.hexdigest()
        except Exception:
            return ""
    
    @staticmethod
    def validate_files(files: List[Path]) -> List[Path]:
        console = Console()
        valid_files = []
        seen_hashes = set()
        for file in files:
            try:
                if not file.exists():
                    console.print(f"[red]❌ File not found: {file}")
                    continue
                if not file.is_file():
                    console.print(f"[red]❌ Not a file: {file}")
                    continue
                if not any(file.name.lower().endswith(ext) for ext in SUPPORTED_EXTENSIONS):
                    console.print(f"[yellow]⚠️  Unsupported file type: {file} (extension: {file.suffix})")
                stat = file.stat()
                file_size = stat.st_size
                if file_size > MAX_FILE_SIZE:
                    console.print(f"[red]❌ File too large ({file_size / 1024 / 1024:.1f}MB): {file}")
                    continue
                if file_size == 0:
                    console.print(f"[red]❌ Empty file: {file}")
                    continue
                file_hash = FileValidator.get_file_hash(file)
                if file_hash and file_hash in seen_hashes:
                    console.print(f"[yellow]⚠️  Duplicate file (same content): {file}")
                    continue
                if file_hash:
                    seen_hashes.add(file_hash)
                if not os.access(file, os.R_OK):
                    console.print(f"[red]❌ Cannot read file: {file}")
                    continue
                valid_files.append(file)
                console.print(f"[green]✅ Valid file: {file} ({file_size / 1024 / 1024:.1f}MB)")
            except Exception as e:
                console.print(f"[red]❌ Error validating {file}: {str(e)}")
        return valid_files

class MessageBuilder:
    """Handles message template building with caching."""
    
    TEMPLATE: Final[str] = """
🚀 **Sing-box {version}**

🏷️ **Tags:** `{tags}`

{update_section}🍒 **Cherry-pick:**
{cherry_pick_commit}

📦 [SagerNet/sing-box](https://github.com/SagerNet/sing-box)
    """.strip()

    def __init__(self, config: Config):
        self.config = config

    @lru_cache(maxsize=1)
    def build(self) -> str:
        """Build message with caching and smart truncation."""
        update_section = f"📝 **Update:**\n{self.config.commit}\n\n" if self.config.commit else ""
        cherry_pick_section = self.config.cherry_pick_commit or "No cherry-pick commits"
        
        msg = self.TEMPLATE.format(
            version=self.config.version,
            tags=self.config.tags or "default",
            update_section=update_section,
            cherry_pick_commit=cherry_pick_section
        )
        
        # Smart truncation with priority
        if len(msg) > MAX_CAPTION_LENGTH:
            # Priority 1: Remove update section
            msg = self.TEMPLATE.format(
                version=self.config.version,
                tags=self.config.tags or "default",
                update_section="",
                cherry_pick_commit=cherry_pick_section
            )
            
            # Priority 2: Truncate cherry-pick section
            if len(msg) > MAX_CAPTION_LENGTH:
                available_length = MAX_CAPTION_LENGTH - len(msg) + len(cherry_pick_section) - 3  # -3 for "..."
                if available_length > 0:
                    cherry_pick_section = cherry_pick_section[:available_length] + "..."
                else:
                    cherry_pick_section = "Cherry-pick commits truncated"
                
                msg = self.TEMPLATE.format(
                    version=self.config.version,
                    tags=self.config.tags or "default",
                    update_section="",
                    cherry_pick_commit=cherry_pick_section
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
            handlers=[RichHandler(rich_tracebacks=True, show_time=True, markup=True)]
        )
        logger = logging.getLogger("telegram-uploader")
        logger.setLevel(logging.INFO)
        return logger

    async def _handle_upload_retry(self, attempt: int, error: Exception) -> None:
        """Handle upload retry logic with exponential backoff."""
        if attempt < MAX_RETRY_ATTEMPTS:
            wait_time = RETRY_DELAY * (2 ** attempt)  # Exponential backoff
            self.logger.warning(f"Attempt {attempt + 1} failed. Retrying in {wait_time} seconds... Error: {error}")
            await asyncio.sleep(wait_time)
        else:
            raise Exception(f"Max retry attempts ({MAX_RETRY_ATTEMPTS}) reached. Last error: {error}")

    async def _test_connection(self, app: Client) -> Dict[str, Any]:
        """Test connection and return bot info."""
        try:
            me = await app.get_me()
            self.logger.info(f"Connected as {me.username} ({me.id})")
            
            # Test chat access
            try:
                chat = await app.get_chat(self.config.chat_id)
                self.logger.info(f"Target chat: {chat.title or chat.first_name} ({chat.id})")
                return {"bot": me, "chat": chat}
            except errors.ChatIdInvalid:
                raise Exception(f"Invalid chat ID: {self.config.chat_id}")
            except errors.ChatNotFound:
                raise Exception(f"Chat not found: {self.config.chat_id}")
            except Exception as e:
                raise Exception(f"Cannot access chat {self.config.chat_id}: {str(e)}")
                
        except errors.AuthKeyUnregistered:
            raise Exception("Bot token is invalid or expired")
        except errors.ApiIdInvalid:
            raise Exception("API ID is invalid")
        except errors.ApiIdPublishedFlood:
            raise Exception("API ID is flood-limited")
        except Exception as e:
            raise Exception(f"Failed to connect to Telegram: {str(e)}")

    async def _pin_message(self, app: Client, message: Message) -> None:
        """Pin message with specific error handling."""
        try:
            await app.pin_chat_message(
                chat_id=self.config.chat_id,
                message_id=message.id,
                disable_notification=True
            )
            self.logger.info(f"Successfully pinned message {message.id}")
        except errors.ChatAdminRequired:
            self.logger.warning("Cannot pin message: Bot is not an admin")
        except errors.ChatNotModified:
            self.logger.info("Message is already pinned")
        except Exception as e:
            self.logger.error(f"Failed to pin message: {e}")

    async def _send_media_group(self, app: Client, media: List[InputMediaDocument]) -> List[Message]:
        """Send media group with enhanced error handling."""
        for attempt in range(MAX_RETRY_ATTEMPTS):
            try:
                return await app.send_media_group(
                    chat_id=self.config.chat_id,
                    media=media,
                    disable_notification=False
                )
            except errors.FloodWait as e:
                self.logger.warning(f"Rate limit hit, waiting {e.value} seconds")
                await asyncio.sleep(e.value)
                continue
            except errors.MediaGroupedInvalid:
                self.logger.error("Media group is invalid, sending files individually")
                return await self._send_files_individually(app, media)
            except errors.MediaInvalid:
                self.logger.error("One or more media files are invalid")
                # Try to identify and skip invalid files
                return await self._send_files_individually(app, media)
            except Exception as e:
                await self._handle_upload_retry(attempt, e)
        
        raise Exception("Failed to send media group after all retries")

    async def _send_files_individually(self, app: Client, media: List[InputMediaDocument]) -> List[Message]:
        """Send files individually as fallback with better error handling."""
        messages = []
        
        for i, media_item in enumerate(media):
            try:
                # Resolve file path properly
                file_path = Path(media_item.media).resolve()
                if not file_path.exists():
                    self.logger.error(f"File not found: {file_path}")
                    continue
                
                # Only add caption to the last file
                caption = media_item.caption if i == len(media) - 1 else ""
                
                # Get MIME type
                mime_type, _ = mimetypes.guess_type(str(file_path))
                
                message = await app.send_document(
                    chat_id=self.config.chat_id,
                    document=str(file_path),
                    caption=caption,
                    parse_mode=enums.ParseMode.MARKDOWN,
                    disable_notification=False,
                    file_name=file_path.name,
                    mime_type=mime_type
                )
                messages.append(message)
                self.logger.info(f"Sent file {i+1}/{len(media)}: {file_path.name}")
                
                # Small delay between individual uploads
                await asyncio.sleep(1)
                
            except errors.MediaInvalid:
                self.logger.error(f"Invalid media file: {media_item.media}")
                continue
            except Exception as e:
                self.logger.error(f"Failed to send file {media_item.media}: {e}")
                continue
        
        return messages

    async def _create_media_groups(self, files: List[Path]) -> List[List[InputMediaDocument]]:
        """Create media groups respecting Telegram limits with better file handling."""
        media_groups = []
        current_group = []
        
        message_template = MessageBuilder(self.config).build()
        
        for i, file in enumerate(files):
            try:
                # Resolve file path properly
                file_path = file.resolve()
                if not file_path.exists():
                    self.logger.error(f"File not found during media group creation: {file_path}")
                    continue
                
                # Add caption only to the last file of the last group
                is_last_file = i == len(files) - 1
                caption = message_template if is_last_file else ""
                
                media = InputMediaDocument(
                    media=str(file_path),
                    caption=caption,
                    parse_mode=enums.ParseMode.MARKDOWN
                )
                
                current_group.append(media)
                
                # If we reach the group size limit or it's the last file, finalize the group
                if len(current_group) >= MAX_MEDIA_GROUP_SIZE or is_last_file:
                    if current_group:  # Only add non-empty groups
                        media_groups.append(current_group)
                    current_group = []
                    
            except Exception as e:
                self.logger.error(f"Error processing file {file}: {e}")
                continue
        
        return media_groups

    async def upload_files(self, files: List[Path]) -> None:
        """Upload files with comprehensive error handling and progress tracking."""
        self.console.print(f"[blue]🚀 Starting upload process for {len(files)} files")
        
        # Validate configuration
        self.config.validate()
        
        # Validate files first
        valid_files = FileValidator.validate_files(files)
        if not valid_files:
            raise ValueError("No valid files to upload")
        
        self.logger.info(f"Uploading {len(valid_files)} valid files")
        
        try:
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
                    BarColumn(),
                    TextColumn("[progress.percentage]{task.percentage:>3.0f}%"),
                    TimeElapsedColumn(),
                    console=self.console
                ) as progress:
                    
                    # Test connection first
                    connection_info = await self._test_connection(app)
                    
                    # Create media groups
                    media_groups = await self._create_media_groups(valid_files)
                    if not media_groups:
                        raise ValueError("No valid media groups created")
                    
                    upload_task = progress.add_task("Preparing upload...", total=len(media_groups))
                    
                    all_messages = []
                    
                    for i, media_group in enumerate(media_groups):
                        group_desc = f"Uploading group {i + 1}/{len(media_groups)}"
                        progress.update(upload_task, description=group_desc)
                        
                        try:
                            messages = await self._send_media_group(app, media_group)
                            all_messages.extend(messages)
                            
                            progress.advance(upload_task)
                            self.logger.info(f"Successfully uploaded group {i + 1}/{len(media_groups)}")
                            
                            # Small delay between groups
                            if i < len(media_groups) - 1:
                                await asyncio.sleep(2)
                            
                        except Exception as e:
                            self.logger.error(f"Failed to upload group {i + 1}: {e}")
                            progress.advance(upload_task)
                            continue
                    
                    # Try to pin the last message
                    if all_messages:
                        await self._pin_message(app, all_messages[-1])
                    
                    progress.update(upload_task, description="Upload completed!")
                    self.console.print(f"[green]✅ Upload completed successfully! Sent {len(all_messages)} messages")
                    
        except Exception as e:
            self.logger.error(f"Upload failed: {str(e)}")
            raise

async def main() -> None:
    """Main function with comprehensive error handling."""
    console = Console()
    
    try:
        # Load configuration
        config = Config.from_env()
        console.print(f"[blue]📋 Configuration loaded for version: {config.version}")
        
        # Get files from command line arguments
        files = [Path(f) for f in sys.argv[1:]]
        if not files:
            raise ValueError("No files specified for upload. Usage: python script.py file1.zip file2.zip ...")
        
        console.print(f"[blue]📁 Found {len(files)} files to process")
        
        # Create uploader and upload files
        uploader = TelegramUploader(config)
        await uploader.upload_files(files)
        
        console.print("[green]🎉 All operations completed successfully!")
        
    except KeyboardInterrupt:
        console.print("[yellow]⚠️  Operation cancelled by user")
        sys.exit(130)
    except ValueError as e:
        console.print(f"[red]❌ Configuration/Input error: {str(e)}")
        sys.exit(1)
    except Exception as e:
        console.print(f"[red]❌ Fatal error: {str(e)}")
        logging.error(f"Fatal error: {str(e)}", exc_info=True)
        sys.exit(1)

if __name__ == "__main__":
    asyncio.run(main())