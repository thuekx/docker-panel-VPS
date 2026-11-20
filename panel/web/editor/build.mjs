import * as esbuild from "esbuild";
await esbuild.build({entryPoints: ["entry.js"], bundle: true, minify: true, format: "iife", globalName: "NostekEditor",
  target: ["es2020"], outfile: "../static/vendor/codemirror.js", legalComments: "eof"});
