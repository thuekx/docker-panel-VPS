// Nostek Panel code editor: CodeMirror 6 (MIT) with the languages of website files.
import {EditorView, keymap, lineNumbers, highlightActiveLineGutter, highlightSpecialChars, drawSelection,
  dropCursor, rectangularSelection, crosshairCursor, highlightActiveLine} from "@codemirror/view";
import {EditorState, Compartment} from "@codemirror/state";
import {foldGutter, indentOnInput, syntaxHighlighting, defaultHighlightStyle, bracketMatching, foldKeymap,
  StreamLanguage, indentUnit} from "@codemirror/language";
import {defaultKeymap, history, historyKeymap, indentWithTab} from "@codemirror/commands";
import {searchKeymap, highlightSelectionMatches, openSearchPanel} from "@codemirror/search";
import {autocompletion, completionKeymap, closeBrackets, closeBracketsKeymap} from "@codemirror/autocomplete";
import {oneDark} from "@codemirror/theme-one-dark";
import {php} from "@codemirror/lang-php";
import {javascript} from "@codemirror/lang-javascript";
import {html} from "@codemirror/lang-html";
import {css} from "@codemirror/lang-css";
import {json} from "@codemirror/lang-json";
import {sql} from "@codemirror/lang-sql";
import {python} from "@codemirror/lang-python";
import {go} from "@codemirror/lang-go";
import {yaml} from "@codemirror/lang-yaml";
import {markdown} from "@codemirror/lang-markdown";
import {xml} from "@codemirror/lang-xml";
import {shell} from "@codemirror/legacy-modes/mode/shell";
import {nginx} from "@codemirror/legacy-modes/mode/nginx";
import {properties} from "@codemirror/legacy-modes/mode/properties";
import {dockerFile} from "@codemirror/legacy-modes/mode/dockerfile";
import {toml} from "@codemirror/legacy-modes/mode/toml";

const byExt = {
  php: () => php(), phtml: () => php(), inc: () => php(),
  js: () => javascript(), mjs: () => javascript(), cjs: () => javascript(), jsx: () => javascript({jsx: true}),
  ts: () => javascript({typescript: true}), tsx: () => javascript({typescript: true, jsx: true}),
  html: () => html(), htm: () => html(), vue: () => html(), twig: () => html(), blade: () => php(),
  css: () => css(), scss: () => css(), less: () => css(),
  json: () => json(), map: () => json(), lock: () => json(),
  sql: () => sql(), py: () => python(), go: () => go(), mod: () => go(),
  yml: () => yaml(), yaml: () => yaml(), md: () => markdown(), markdown: () => markdown(),
  xml: () => xml(), svg: () => xml(), xsl: () => xml(),
  sh: () => StreamLanguage.define(shell), bash: () => StreamLanguage.define(shell),
  conf: () => StreamLanguage.define(nginx), env: () => StreamLanguage.define(properties),
  ini: () => StreamLanguage.define(properties), properties: () => StreamLanguage.define(properties),
  htaccess: () => StreamLanguage.define(properties), toml: () => StreamLanguage.define(toml),
};

// language for a file name: the extension, a few known names, otherwise plain text
export function langFor(name) {
  const base = (name || "").split("/").pop().toLowerCase();
  if (base === "dockerfile" || base.startsWith("dockerfile.")) return {key: "dockerfile", ext: StreamLanguage.define(dockerFile)};
  if (base.endsWith(".blade.php")) return {key: "php", ext: php()};
  if (base === ".env" || base.startsWith(".env.")) return {key: "env", ext: byExt.env()};
  if (base === ".htaccess" || base === ".user.ini") return {key: "ini", ext: byExt.ini()};
  const i = base.lastIndexOf(".");
  const e = i >= 0 ? base.slice(i + 1) : "";
  if (byExt[e]) return {key: e, ext: byExt[e]()};
  return {key: "text", ext: []};
}

const theme = new Compartment(), wrap = new Compartment();
const dark = () => document.documentElement.getAttribute("data-theme") === "dark" ||
  (document.documentElement.getAttribute("data-theme") !== "light" && window.matchMedia("(prefers-color-scheme: dark)").matches);

// mount replaces a textarea with the editor; the textarea stays the form field and gets the text on change
export function mount(ta, opts) {
  opts = opts || {};
  const lang = langFor(opts.name || "");
  const usesTabs = /^\t/m.test(ta.value) || !/^ {2}/m.test(ta.value);
  const view = new EditorView({
    parent: ta.parentNode,
    state: EditorState.create({
      doc: ta.value,
      extensions: [
        lineNumbers(), highlightActiveLineGutter(), highlightSpecialChars(), history(), foldGutter(), drawSelection(),
        dropCursor(), EditorState.allowMultipleSelections.of(true), indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle, {fallback: true}), bracketMatching(), closeBrackets(),
        autocompletion(), rectangularSelection(), crosshairCursor(), highlightActiveLine(), highlightSelectionMatches(),
        indentUnit.of(usesTabs ? "\t" : "    "), EditorState.tabSize.of(4),
        keymap.of([
          {key: "Mod-s", preventDefault: true, run: () => { if (opts.onSave) opts.onSave(); return true; }},
          ...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap,
          ...completionKeymap, indentWithTab]),
        opts.nonce ? EditorView.cspNonce.of(opts.nonce) : [],
        lang.ext, theme.of(dark() ? oneDark : []), wrap.of([]),
        EditorView.updateListener.of(u => { if (u.docChanged) { ta.value = u.state.doc.toString(); if (opts.onChange) opts.onChange(); } }),
        EditorView.contentAttributes.of({"aria-label": ta.getAttribute("aria-label") || "editor", spellcheck: "false", autocapitalize: "off", autocorrect: "off"}),
      ],
    }),
  });
  ta.parentNode.insertBefore(view.dom, ta);
  ta.hidden = true;
  let wrapped = false;
  return {
    view, lang: lang.key,
    setWrap(on) { wrapped = on; view.dispatch({effects: wrap.reconfigure(on ? EditorView.lineWrapping : [])}); },
    toggleWrap() { this.setWrap(!wrapped); return wrapped; },
    search() { openSearchPanel(view); },
    syncTheme() { view.dispatch({effects: theme.reconfigure(dark() ? oneDark : [])}); },
    focus() { view.focus(); },
  };
}
