import json
import os
import time
import urllib.error
import urllib.request


def call_api(base, method, parameters):
    payload = json.dumps(parameters).encode("utf-8")
    request = urllib.request.Request(
        base + "/" + method,
        data=payload,
        headers={"Content-Type": "application/json"},
    )
    response = None
    data = None
    with urllib.request.urlopen(request, timeout=60) as response:
        data = json.load(response)
    if not data.get("ok"):
        raise ValueError("Bot API rejected the request")
    return data["result"]


def main():
    instance = os.environ.get("GREENGRAM_URL", "").rstrip("/")
    token = os.environ.get("BOT_TOKEN", "")
    base = instance + "/bot" + token
    offset = 0
    updates = []
    update = None
    message = None
    callback = None
    text = ""
    error = None
    if not instance or not token:
        raise SystemExit("Set GREENGRAM_URL and BOT_TOKEN before running this bot")
    while True:
        try:
            updates = call_api(base, "getUpdates", {"offset": offset, "timeout": 50})
            for update in updates:
                callback = update.get("callback_query")
                if callback:
                    call_api(base, "answerCallbackQuery", {
                        "callback_query_id": callback["id"],
                        "text": "Your button works!",
                    })
                message = update.get("message")
                if message and message.get("text"):
                    text = message["text"]
                    if text.startswith("/start"):
                        text = "Hello! Send me a message and I will echo it."
                    call_api(base, "sendMessage", {
                        "chat_id": message["chat"]["id"],
                        "text": text,
                        "reply_markup": {"inline_keyboard": [[{
                            "text": "Test button",
                            "callback_data": "hello",
                        }]]},
                    })
                offset = update["update_id"] + 1
        except urllib.error.HTTPError as error:
            if error.code in (401, 403, 409):
                raise SystemExit("Bot API returned HTTP " + str(error.code)) from None
            print("Bot API returned HTTP " + str(error.code) + "; retrying", flush=True)
            time.sleep(3)
        except (urllib.error.URLError, TimeoutError, ValueError):
            print("Bot API request failed; retrying", flush=True)
            time.sleep(3)
        except KeyboardInterrupt:
            return


if __name__ == "__main__":
    main()
