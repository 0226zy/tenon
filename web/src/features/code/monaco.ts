import EditorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker'
import JsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker'
import CssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker'
import HtmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker'
import TsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker'

const globalScope = self as unknown as {
  MonacoEnvironment?: { getWorker: (moduleId: string, label: string) => Worker }
}
globalScope.MonacoEnvironment = {
  getWorker: (_moduleId, label) => {
    if (label === 'json') return new JsonWorker()
    if (['css', 'scss', 'less'].includes(label)) return new CssWorker()
    if (['html', 'handlebars', 'razor'].includes(label)) return new HtmlWorker()
    if (['typescript', 'javascript'].includes(label)) return new TsWorker()
    return new EditorWorker()
  },
}

let pending: Promise<typeof import('monaco-editor')> | undefined
export function loadMonaco(): Promise<typeof import('monaco-editor')> {
  if (!pending) pending = import('monaco-editor')
  return pending
}
