// 收集页面上的 CSP 违规（壳的 CSP 在 M6-3 和取图协议一起做）。`--smoke` 自检把它们放进结果里，有违规就算失败。

export interface CspViolation {
  directive: string
  blocked: string
  source: string
}

const violations: CspViolation[] = []
let watching = false

/** 开始收集（尽早调用，main.ts 一开头）。 */
export function watchCspViolations() {
  if (watching || typeof document === 'undefined') return
  watching = true
  document.addEventListener('securitypolicyviolation', (e) => {
    const v: CspViolation = {
      directive: e.effectiveDirective || e.violatedDirective,
      blocked: e.blockedURI,
      source: e.sourceFile ? `${e.sourceFile}:${e.lineNumber}` : '',
    }
    violations.push(v)
    console.error('CSP 违规', v)
  })
}

export function cspViolations(): readonly CspViolation[] {
  return violations
}
