import asyncio
import os
import sys
from telethon import TelegramClient
from telethon.tl.functions.help import GetConfigRequest

API_ID = os.environ.get("API_ID")
API_HASH = os.environ.get("API_HASH")
CHAT_ID = int(os.environ.get("CHAT_ID", 0))
BOT_TOKEN = os.environ.get("BOT_TOKEN")
VERSION = os.environ.get("VERSION")
COMMIT = os.environ.get("COMMIT")
CHERRY_PICK_COMMIT = os.environ.get("CHERRY_PICK_COMMIT")
TAGS = os.environ.get("TAGS")
MSG_TEMPLATE = """
Sing-box {version}

Tags: {tags}

Update:
{commit}

Cherry-pick:
{cherry_pick_commit}

[SagerNet/sing-box](https://github.com/SagerNet/sing-box)
""".strip()

MSG_NO_COMMIT_TEMPLATE = """
Sing-box {version}

Tags: {tags}

Cherry-pick:
{cherry_pick_commit}

[SagerNet/sing-box](https://github.com/SagerNet/sing-box)
""".strip()

def get_caption():
    if COMMIT != "":
        msg = MSG_TEMPLATE.format(
            version=VERSION,
            tags=TAGS,
            commit=COMMIT,
            cherry_pick_commit=CHERRY_PICK_COMMIT
        )
    else:
        msg = MSG_NO_COMMIT_TEMPLATE.format(
            version=VERSION,
            tags=TAGS,
            cherry_pick_commit=CHERRY_PICK_COMMIT
        )
    if len(msg) > 1024:
        return MSG_NO_COMMIT_TEMPLATE.format(
            version=VERSION,
            tags=TAGS,
            cherry_pick_commit=CHERRY_PICK_COMMIT
        )
    return msg

def check_environ():
    if not BOT_TOKEN:
        print("[-] Invalid BOT_TOKEN")
        sys.exit(1)
    if not CHAT_ID:
        print("[-] Invalid CHAT_ID")
        sys.exit(1)
    if not VERSION:
        print("[-] Invalid VERSION")
        sys.exit(1)
    if not TAGS:
        print("[-] Invalid TAGS")
        sys.exit(1)

async def main():
    print("[+] Checking environment variables")
    check_environ()

    files = sys.argv[1:]
    if not files:
        print("[-] No files to upload")
        sys.exit(1)
        
    print("[+] Files:", files)
    print("[+] Logging in Telegram with bot")
    script_dir = os.path.dirname(os.path.abspath(sys.argv[0]))
    session_dir = os.path.join(script_dir, "bot.session")
    async with await TelegramClient(session=session_dir, api_id=API_ID, api_hash=API_HASH).start(bot_token=BOT_TOKEN) as bot:
        caption = [""] * len(files)
        caption[-1] = get_caption()
        print("[+] Caption: ")
        print("---")
        print(caption)
        print("---")
        print("[+] Sending")
        sent_messages = await bot.send_file(entity=CHAT_ID, file=files, caption=caption, parse_mode="markdown")

        # Pin the last sent message
        if sent_messages:
            last_message_id = sent_messages[-1].id
            await bot.pin_message(entity=CHAT_ID, message=last_message_id)

        print("[+] Done!")

if __name__ == "__main__":
    try:
        asyncio.run(main())
    except Exception as e:
        print(f"[-] An error occurred: {e}")
        sys.exit(1)