import sys, os
from fontTools.ttLib import TTFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
OUT=sys.argv[1]

def text_path(txt, font_file, cap_h, x0, baseline, tracking_em=0.0):
    f=TTFont(font_file); gs=f.getGlyphSet(); cmap=f.getBestCmap()
    upm=f['head'].unitsPerEm; cap=f['OS/2'].sCapHeight; s=cap_h/cap
    pen=SVGPathPen(gs); x=x0
    for ch in txt:
        g=cmap[ord(ch)]
        tp=TransformPen(pen,(s,0,0,-s,x,baseline)); gs[g].draw(tp)
        x+=gs[g].width*s + tracking_em*upm*s
    x-=tracking_em*upm*s
    return pen.getCommands(), x

# ---- the N mark (box 0..100), 180-degree symmetric around (50,50)
MARK = {
 'band_low':  "M32,38 L92,80 L74,91.4 L32,62 Z",
 'pillar_r':  "M72,20 L92,6 L92,80 L72,66 Z",
 'pillar_l':  "M8,20 L28,34 L28,80 L8,94 Z",
 'shadow':    "M32,37.5 L69,63.2 L69,65 L32,46 Z",
 'band_top':  "M8,20 L26,8.6 L68,38 L68,62 Z",
}
# wordmark NOSTEK (own geometric letters, cap height 50); E middle bar is the accent
WM_WHITE = [
 "M3,3 Q3,0 6,0 H21 L47,32 V0 H65 V47 Q65,50 62,50 H47 L21,18 V50 H3 Z",                       # N
 "M71,9 Q71,0 80,0 H121 Q130,0 130,9 V41 Q130,50 121,50 H80 Q71,50 71,41 Z M89,14 V36 H112 V14 Z", # O
 "M136,10 Q136,0 146,0 H191 V11 H152 V17.5 H181 Q191,17.5 191,27.5 V40 Q191,50 181,50 H136 V38.5 H174 V31 H146 Q136,31 136,21 Z", # S
 "M194,0 H244 V12.5 H227 V50 H209 V12.5 H194 Z",                                                     # T
 "M247,9 Q247,0 256,0 H302 V12.5 H267 Q259,12.5 253,17.5 L247,22.5 Z",                                # E top
 "M247,31 H265 V37.5 H302 V50 H253 Q247,50 247,44 Z",                                                # E bottom
 "M307,0 H325 V50 H307 Z M327,25 L351,0 H373 L348,25 L373,50 H351 Z",                                # K
]
WM_ACCENT = "M247,31 V29.5 L255,22.5 Q258,20 263,20 H294 V31 Z"
WM_W=373

THEMES = {
 # name: (N gradient stops, accent, label)
 'blue':   dict(c1='#14E6FF', c2='#0A6CFF', c3='#0040E0', acc='#12D8FF', accl='#0A6CFF'),
 'violet': dict(c1='#C4A6FF', c2='#7C4DFF', c3='#4A1FD1', acc='#A98BFF', accl='#6A3DF0'),
 'teal':   dict(c1='#5EF2D0', c2='#0FB5A6', c3='#0A6E8A', acc='#2EE6C5', accl='#0B9483'),
 'mono-light': dict(mono='#0B1530'),
 'mono-dark':  dict(mono='#FFFFFF'),
}

def defs(t, p):
    if 'mono' in t: return ''
    return f'''<defs>
 <linearGradient id="{p}t" x1="8" y1="10" x2="68" y2="60" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="{t['c1']}"/><stop offset=".6" stop-color="{t['c1']}"/><stop offset="1" stop-color="{t['c2']}"/></linearGradient>
 <linearGradient id="{p}b" x1="32" y1="45" x2="90" y2="88" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="{t['c3']}"/><stop offset=".6" stop-color="{t['c2']}"/><stop offset="1" stop-color="{t['c1']}"/></linearGradient>
 <linearGradient id="{p}l" x1="0" y1="20" x2="0" y2="94" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="{t['c3']}"/><stop offset=".5" stop-color="{t['c2']}"/><stop offset="1" stop-color="{t['c3']}"/></linearGradient>
 <linearGradient id="{p}r" x1="0" y1="6" x2="0" y2="80" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="{t['c3']}"/><stop offset="1" stop-color="{t['c2']}"/></linearGradient>
 <linearGradient id="{p}s" x1="32" y1="40" x2="69" y2="64" gradientUnits="userSpaceOnUse"><stop offset="0" stop-color="#020A2A" stop-opacity=".85"/><stop offset="1" stop-color="#020A2A" stop-opacity=".2"/></linearGradient>
</defs>'''

def mark(t, p, tf=''):
    if 'mono' in t:
        m=t['mono']; o=lambda a: f'fill="{m}" fill-opacity="{a}"'
        parts=[('band_low',o(.7)),('pillar_r',o(.85)),('pillar_l',o(.85)),('band_top',o(1))]
    else:
        parts=[('band_low',f'fill="url(#{p}b)"'),('pillar_r',f'fill="url(#{p}r)"'),('pillar_l',f'fill="url(#{p}l)"'),('shadow',f'fill="url(#{p}s)"'),('band_top',f'fill="url(#{p}t)"')]
    return f'<g{tf}>' + ''.join(f'<path d="{MARK[k]}" {a}/>' for k,a in parts) + '</g>'

FONT='/usr/share/fonts/opentype/inter/InterDisplay-SemiBold.otf'
def horizontal(theme, suffix, bg):
    t=THEMES[theme]; p=theme[:2]
    ink = '#FFFFFF' if bg=='dark' else '#0B1530'
    acc = t.get('acc', t.get('mono')) if bg=='dark' else t.get('accl', t.get('mono'))
    if 'mono' in t: ink=t['mono']
    # mark 0..100 ; wordmark scaled 0.96 at x=118, top y=26
    s=0.96; wx=118; wy=26
    wm = f'<g transform="translate({wx},{wy}) scale({s}) translate(-3,0)">' + ''.join(f'<path d="{d}" fill="{ink}"/>' for d in WM_WHITE) + f'<path d="{WM_ACCENT}" fill="{acc}"/></g>'
    x_end = wx + (WM_W-3)*s
    W = x_end
    suf=''
    if suffix:
        d, xe = text_path(suffix, FONT, 26, x_end+16, wy+50*s, tracking_em=0.12)
        suf=f'<path d="{d}" fill="{acc}"/>'; W=xe
    W=round(W+8); H=100
    bgrect = '' if bg=='none' else f'<rect width="100%" height="100%" fill="{"#0B1530" if bg=="dark" else "#FFFFFF"}"/>'
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="-4 0 {W+4} {H}" width="{(W+4)*2}" height="{H*2}" role="img" aria-label="Nostek{(" "+suffix.title()) if suffix else ""}"><title>Nostek{(" "+suffix.title()) if suffix else ""}</title>{defs(t,p)}{bgrect}{mark(t,p)}{wm}{suf}</svg>'

def icon(theme, bg):
    t=THEMES[theme]; p=theme[:2]
    if bg=='none':
        return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="512" height="512" role="img" aria-label="Nostek"><title>Nostek</title>{defs(t,p)}{mark(t,p)}</svg>'
    fill = '#0B1530' if bg=='dark' else '#FFFFFF'
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 120" width="512" height="512" role="img" aria-label="Nostek"><title>Nostek</title>{defs(t,p)}<rect width="120" height="120" rx="26" fill="{fill}"/>{mark(t,p,' transform="translate(14,14) scale(.92)"')}</svg>'

os.makedirs(OUT, exist_ok=True)
files={}
files['nostek-mark.svg']=icon('blue','none')
files['nostek-app-icon.svg']=icon('blue','dark')
files['nostek-favicon.svg']=icon('blue','dark')
for theme,suf,slug in [('blue','PANEL','panel'),('violet','CONTROL PANEL','control-panel'),('teal','DOCKER PANEL','docker-panel')]:
    files[f'nostek-{slug}-dark.svg']=horizontal(theme,suf,'none-dark')  if False else horizontal(theme,suf,'dark')
    files[f'nostek-{slug}-light.svg']=horizontal(theme,suf,'light')
files['nostek-panel-mono-dark.svg']=horizontal('mono-dark','PANEL','dark')
files['nostek-panel-mono-light.svg']=horizontal('mono-light','PANEL','light')
# transparent README versions
def transparent(theme,suf,ink_bg): 
    s=horizontal(theme,suf,ink_bg); import re
    return re.sub(r'<rect width="100%" height="100%"[^>]*/>','',s)
files['nostek-panel-on-dark.svg']=transparent('blue','PANEL','dark')
files['nostek-panel-on-light.svg']=transparent('blue','PANEL','light')
for n,c in files.items(): open(os.path.join(OUT,n),'w').write(c)
print('\n'.join(sorted(files)))
