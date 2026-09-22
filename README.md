# Pulse

A Telegram bot that keeps your LinkedIn presence alive without the daily grind.
It watches your sources, drafts posts with an LLM, reminds you on a schedule
over Telegram DM, and publishes to LinkedIn when you approve.

Pulse is self-hosted and single-owner: you run your own copy with your own
Telegram bot, and the first person to send `/start` owns it. Nobody else
can use your bot, and you cannot use someone else's. To get your own,
follow the three steps below.

## How it works

```mermaid
flowchart LR
    subgraph Telegram
        DM[You, DMs on phone]
    end
    subgraph Bot[Go binary, one process]
        Router[Command and button router]
        Scheduler[Scheduler loop]
        Feeds[Feed ingester]
        Gen[Post generator]
        Pub[Publisher]
        Store[(SQLite)]
        OAuth[OAuth callback, tiny HTTP server]
    end
    subgraph External
        RSS[RSS and links]
        LLM[LLM provider]
        LIN[LinkedIn API]
    end
    DM --> Router
    Scheduler --> Feeds --> RSS
    Scheduler --> Gen --> LLM
    Scheduler --> DM
    Router --> Store
    Router --> Pub --> LIN
    OAuth --> LIN
```

On schedule the bot DMs you a draft card with buttons: **Approve** posts it
to LinkedIn and returns the live link, **Edit** asks you to reply with
revised text, **Regenerate** draws another source item, **Skip** drops it, **Snooze 1h**
brings it back in an hour.

## Quickstart

Prerequisites: Go 1.26 or newer and a Telegram account.

```sh
git clone https://github.com/PraisejahOsumgbaBenson/pulse.git
cd pulse
cp .env.example .env   # add your Telegram token, see Setup step 1
go run ./cmd/bot
```

Then message your bot: `/start` to claim it, `/topic` followed by any
thought for an instant draft, `/schedule` to set reminders step by step.

## Setup

### 1. Telegram bot, about 1 minute

1. Open Telegram and message [@BotFather](https://t.me/BotFather).
   Send `/newbot`, follow the prompts, and copy the token into
   `TELEGRAM_TOKEN`.
2. Message your new bot with `/start`. If `TELEGRAM_OWNER_ID` is empty,
   you become the owner automatically. To lock it instead, message
   [@userinfobot](https://t.me/userinfobot), copy your numeric id into
   `TELEGRAM_OWNER_ID`, and restart.

No intents, no permissions dance, no server invites. One token is the
whole credential, and everything works in a plain 1:1 chat, including
on the phone app.

### 2. LinkedIn developer app, about 10 minutes (optional at first)

Without LinkedIn connected, Pulse still reminds you and drafts posts;
tapping Approve hands you the text to paste into the LinkedIn app
yourself. Connect the app whenever auto posting matters:

1. Open the [LinkedIn Developer Portal](https://www.linkedin.com/developers/),
   create an app (this asks for a LinkedIn Page), and on the Products tab
   add **Sign In with LinkedIn** and **Share on LinkedIn**. Both are
   self-serve for posting to your own profile, no review queue.
2. On the Auth tab add your callback URL to Authorized redirect URLs, e.g.
   `http://localhost:8081/oauth/linkedin/callback` for local runs or
   `https://your-host/oauth/linkedin/callback` in production. Copy the
   Client ID and Client Secret into the env file.
3. Message the bot `/link`, open the connect link, sign in to LinkedIn and
   approve. The bot confirms when it is linked.

One honest limitation: LinkedIn consumer tokens expire after about 60 days
and consumer apps get no refresh token. Pulse warns you three days ahead,
daily once expired, and reconnecting is one `/link` tap. Everything else
survives untouched.

### 3. Environment

See [.env.example](.env.example) for every variable. To start, only
`TELEGRAM_TOKEN` is required; LinkedIn values can wait until you want
auto posting. Leave `LLM_API_KEY` empty to use the built-in offline
generator. For free AI drafts, create a key at
[Google AI Studio](https://aistudio.google.com), set `LLM_API_KEY` to it,
`LLM_BASE_URL` to `https://generativelanguage.googleapis.com/v1beta/openai`
and `LLM_MODEL` to a flash model such as `gemini-3.5-flash-lite`. Any
OpenAI-compatible provider works the same way. `LLM_STYLE` tunes the
voice, e.g. warm and conversational.

## Commands

| Command | What it does |
|---|---|
| `/start` | Claim the bot and see what Pulse does |
| `/link` | Connect or reconnect LinkedIn |
| `/status` | LinkedIn, schedule, sources, drafts at a glance |
| `/source_add <url>` | Add an RSS feed or page |
| `/source_list` | Show sources with ids |
| `/source_remove <id>` | Drop a source |
| `/schedule_set <time> <days>` | Set reminders, e.g. `09:00 monday wednesday friday`, also `daily`, `weekdays`, `weekends`, add `autopost` at the end to skip approval |
| `/schedule` | Set reminders step by step with questions |
| `/schedule_status` | Show the schedule |
| `/schedule_off` | Stop reminders |
| `/draft` | Generate one right now |
| `/topic <thought>` | Draft from a rough idea, no sources needed (or just type the thought) |
| `/trending` | See what's hot across your feeds, reply with a number to draft it |
| `/history [limit]` | Recent drafts and posts |
| `/autopost <true\|false>` | Post without approval at schedule time |
| `/cancel` | Drop a pending edit |
| `/help` | Command list |

## Deploy

Docker, one static binary with SQLite on a volume:

```sh
docker build -t pulse .
docker run -d --name pulse --env-file .env -v pulse-data:/data -p 8081:8081 pulse
```

A Fly.io example lives in [deploy/fly.toml](deploy/fly.toml): create a
volume for `/data`, set secrets with `fly secrets set`, deploy. The callback
URL registered in your LinkedIn app must match the public host.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

Layout: `cmd/bot` wires everything; `internal/config` env loading;
`internal/store` SQLite; `internal/linkedin` OAuth and Posts API;
`internal/feeds` RSS and page ingestion; `internal/generate` LLM client and
fallback; `internal/publisher` posting with refresh; `internal/telegram`
Bot API client, commands and draft cards; `internal/scheduler` the reminder loop.

Branch off `main` for changes, conventional commits, squash merge. Docs stay
in sync with behavior in the same PR.

## License

MIT. See [LICENSE](LICENSE).
