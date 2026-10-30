"""Renders docs/architecture.png (run: python3 scripts/architecture.py)."""
from PIL import Image, ImageDraw, ImageFont

S = 2  # supersample
W, H = 1600 * S, 900 * S
BG, INK, MUTED, LINE = "#f6f8fa", "#0f172a", "#475569", "#cbd5e1"
TEAL, TEAL_SOFT, BLUE, BLUE_SOFT, AMBER_SOFT, VIOLET_SOFT = "#0d9488", "#ccfbf1", "#2563eb", "#dbeafe", "#fef3c7", "#ede9fe"
F = "/usr/share/fonts/opentype/inter/"
def font(size, weight="Regular"):
    return ImageFont.truetype(F + f"Inter-{weight}.otf", size * S)
f_title, f_h, f_b, f_s, f_tag = font(30, "SemiBold"), font(19, "SemiBold"), font(15), font(13), font(12, "Medium")

im = Image.new("RGB", (W, H), BG)
d = ImageDraw.Draw(im)

def box(x, y, w, h, fill, title, lines, accent=None, tag=None):
    d.rounded_rectangle((x*S, y*S, (x+w)*S, (y+h)*S), radius=14*S, fill=fill, outline=LINE, width=2*S)
    if accent:
        d.rounded_rectangle((x*S, y*S, (x+6)*S, (y+h)*S), radius=6*S, fill=accent)
    d.text(((x+18)*S, (y+14)*S), title, fill=INK, font=f_h)
    if tag:
        tw = d.textlength(tag, font=f_tag) / S
        d.rounded_rectangle(((x+w-tw-30)*S, (y+15)*S, (x+w-12)*S, (y+37)*S), radius=11*S, fill="white", outline=LINE, width=S)
        d.text(((x+w-tw-21)*S, (y+19)*S), tag, fill=MUTED, font=f_tag)
    yy = y + 46
    for ln in lines:
        if ln == "":
            yy += 8
            continue
        bullet = ln.startswith("- ")
        txt = ln[2:] if bullet else ln
        fnt = f_b if bullet else f_s
        col = INK if bullet else MUTED
        if bullet:
            d.ellipse(((x+20)*S, (yy+8)*S, (x+25)*S, (yy+13)*S), fill=TEAL)
        d.text(((x+(32 if bullet else 18))*S, yy*S), txt, fill=col, font=fnt)
        yy += 24 if bullet else 21
    return (x, y, w, h)

def arrow(p1, p2, label=None, color=INK, dashed=False, above=True):
    (x1, y1), (x2, y2) = p1, p2
    if dashed:
        import math
        L = math.hypot(x2-x1, y2-y1); n = int(L // 10)
        for i in range(0, n, 2):
            a = i / n; b = min((i+1)/n, 1)
            d.line(((x1+(x2-x1)*a)*S, (y1+(y2-y1)*a)*S, (x1+(x2-x1)*b)*S, (y1+(y2-y1)*b)*S), fill=color, width=3*S)
    else:
        d.line((x1*S, y1*S, x2*S, y2*S), fill=color, width=3*S)
    import math
    ang = math.atan2(y2-y1, x2-x1)
    for sgn in (1, -1):
        d.line((x2*S, y2*S, (x2-12*math.cos(ang-sgn*0.45))*S, (y2-12*math.sin(ang-sgn*0.45))*S), fill=color, width=3*S)
    if label:
        mx, my = (x1+x2)/2, (y1+y2)/2
        tw = d.textlength(label, font=f_s) / S
        d.rounded_rectangle(((mx-tw/2-8)*S, (my-(26 if above else -4))*S, (mx+tw/2+8)*S, (my-(4 if above else -26))*S), radius=8*S, fill="white", outline=LINE, width=S)
        d.text(((mx-tw/2)*S, (my-(23 if above else -7))*S), label, fill=MUTED, font=f_s)

d.text((40*S, 28*S), "Mandate: how the pieces fit", fill=INK, font=f_title)
d.text((40*S, 70*S), "The agent researches and proposes. The policy engine decides. A human approves. PayPal moves the money.", fill=MUTED, font=f_b)

# column 1: people and UI
box(40, 130, 270, 150, "white", "You", [
    "- set the budget, caps and rules",
    "- give the agent a goal",
    "- approve or reject, with feedback",
    "- pay at PayPal (once, if saved)",
], accent=TEAL)
box(40, 320, 270, 170, "white", "Web UI", [
    "- React + TypeScript",
    "- AG Grid: candidates, cart, ledger",
    "- AG Charts: spending insights",
    "- live activity (server-sent events)",
    "  light and dark themes",
], accent=BLUE, tag="browser")

# column 2: the server
d.rounded_rectangle((400*S, 120*S, 1100*S, 845*S), radius=18*S, fill="white", outline=TEAL, width=3*S)
d.text((424*S, 136*S), "Mandate server (Go)", fill=INK, font=f_h)
d.text((424*S, 162*S), "one binary, JSON persistence, runs anywhere", fill=MUTED, font=f_s)
box(424, 195, 320, 165, TEAL_SOFT, "Policy engine", [
    "- budget, per-item cap, threshold",
    "- categories, blocked merchants, rating",
    "- re-run at approval time",
    "- the only thing that says yes",
])
box(760, 195, 316, 165, TEAL_SOFT, "Budget & ledger", [
    "- reserve on order, spend on capture",
    "- release on cancel, void, refund",
    "- every order, capture and payer",
    "- serialised money transitions",
])
box(424, 385, 652, 190, AMBER_SOFT, "Shopping agent · Claude Opus 5.5 (Anthropic API)", [
    "- tools: search_products · get_product · check_mandate · propose_cart",
    "- no payment tool exists: it can only research, compare and propose",
    "- rejected with feedback? it revises in the same conversation",
    "- prompt caching, server-side model fallback",
], tag="agent loop")
box(424, 600, 320, 225, VIOLET_SOFT, "Order assistant", [
    "- Claude + PayPal's MCP server",
    "- via the Anthropic MCP connector",
    "- read-only tools only: orders,",
    "  transactions, tracking, refunds",
    "- can look, never pay",
], tag="MCP")
box(760, 600, 316, 225, BLUE_SOFT, "Payments", [
    "- Orders v2: create → approve → capture",
    "- idempotent via PayPal-Request-Id",
    "- vault: save at first checkout,",
    "  then one-click and no-click pay",
    "- verified webhooks, reconciliation",
], tag="server pays")

# column 3: outside services
box(1230, 130, 330, 130, "white", "Channel3", [
    "- live product search API",
    "- offers, prices, merchants, ratings",
    "- (demo catalog when offline)",
], accent=TEAL, tag="catalog")
box(1230, 300, 330, 110, "white", "Anthropic API", [
    "- claude-opus-5-5, tool use",
    "- MCP connector to PayPal",
], accent=AMBER_SOFT, tag="model")
box(1230, 450, 330, 210, "white", "PayPal", [
    "- sandbox checkout: buyer consent",
    "- Orders v2, capture, vault tokens",
    "- webhooks: approved, captured, refunded",
    "- hosted MCP server (Agent Toolkit)",
    "  mcp.sandbox.paypal.com",
], accent=BLUE, tag="money moves here")
box(1230, 700, 330, 125, "white", "Deployment", [
    "- Linux server via GitHub release",
    "- Docker Compose · Render blueprint",
    "- go test ./... runs the whole flow",
], accent=LINE)

# arrows
arrow((310, 205), (400, 205))
arrow((310, 400), (400, 400), "API + SSE")
arrow((175, 280), (175, 320))
arrow((1100, 430), (1230, 200), "catalog search")
arrow((1100, 490), (1230, 360), "messages + tools")
arrow((1100, 712), (1230, 560), "orders · vault")
arrow((1395, 450), (1395, 410), dashed=True)
d.text((1408*S, 423*S), "MCP connector, read-only", fill=MUTED, font=f_s)
d.text((40*S, 862*S), "Two gates guard the money: the policy engine (re-run at approval) and the buyer's own PayPal consent (per checkout, or once when the account is saved).", fill=MUTED, font=f_s)

im = im.resize((W//S, H//S), Image.LANCZOS)
im.save("docs/architecture.png", optimize=True)
print("docs/architecture.png", im.size)
