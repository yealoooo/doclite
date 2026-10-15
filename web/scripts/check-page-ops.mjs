#!/usr/bin/env node
// 页序操作纯函数（src/views/catalog/page-ops.ts，M3-6）的检查：`npm run check:page-ops`。
// 没有单元测试框架，这里用 node 直接导入 .ts（Node 23 自带去掉类型注解）逐条断言，失败时退出码 1。
// 重点是 409 conflict 之后"在最新的页序上重做"：挨着的文件不在了时按备用下标放（M3-6 审查第 1 轮：
// "插到选中页之后"的备用下标原来多了一位）。

import { applyOp, resolveTarget, targetAtGap } from '../src/views/catalog/page-ops.ts'

let failed = 0
let passed = 0
function eq(what, got, want) {
  const g = JSON.stringify(got)
  const w = JSON.stringify(want)
  if (g === w) passed++
  else {
    failed++
    console.error(`✗ ${what}：得到 ${g}，应为 ${w}`)
  }
}
const s = (list) => list.join('')
const L = ['a', 'b', 'c', 'd', 'e']
const N = { file_id: 'N', file_name: 'N.png', review_status: 'pending', review_round: 1, version_no: 1 }
const M = { ...N, file_id: 'M', file_name: 'M.png' }

// ---- 插到选中页之后：选中 b（第 2 页，下标 1）→ { after: 'b', index: 1 }（ItemDetail.linkFiles 用 page_no - 1） ----
const insAfterB = { kind: 'insert', files: [N], target: { after: 'b', index: 1 }, label: '' }
eq('插到 b 之后', s(applyOp(insAfterB, L).list), 'abNcde')
eq('插到 b 之后，b 被别人移出了：放在 b 原来的位置', s(applyOp(insAfterB, ['a', 'c', 'd', 'e']).list), 'aNcde')
eq('插到 e（最后一页）之后，e 不在了：放在最后', s(applyOp({ ...insAfterB, target: { after: 'e', index: 4 } }, ['a', 'b', 'c', 'd']).list), 'abcdN')
eq('插到 b 之后，别人在 b 前面加了 X：还是 b 后面', s(applyOp(insAfterB, ['X', 'a', 'b', 'c']).list), 'XabNc')

// ---- 拖到两页之间：targetAtGap ----
eq('第 2 个空隙（c 前面）', targetAtGap(L, 2), { before: 'c', index: 2 })
eq('最后的空隙', targetAtGap(L, 5), { at: 'end' })
eq('拖 b 到 d 前面（空隙 3，去掉 b 之后 d 的下标 2）', targetAtGap(L, 3, new Set(['b'])), { before: 'd', index: 2 })
eq('拖 d、e 到最后（空隙 5）：后面没有不动的页', targetAtGap(L, 5, new Set(['d', 'e'])), { at: 'end' })
eq('拖 d 到 d 自己前面的空隙：挨着的是 e', targetAtGap(L, 3, new Set(['d'])), { before: 'e', index: 3 })
const insBeforeC = { kind: 'insert', files: [N, M], target: targetAtGap(L, 2), label: '' }
eq('从池里拖 N、M 到 c 前面', s(applyOp(insBeforeC, L).list), 'abNMcde')
eq('c 被别人移出了：放在 c 原来的位置', s(applyOp(insBeforeC, ['a', 'b', 'd', 'e']).list), 'abNMde')

// ---- 挪动：多选保持原来的相对顺序 ----
const mv2 = { kind: 'move', fileIds: ['d', 'b'], target: targetAtGap(L, 0, new Set(['b', 'd'])), label: '' }
eq('d、b 一起拖到 a 前面：按页序（b 在 d 前）', s(applyOp(mv2, L).list), 'bdace')
eq('同上，a 被别人移出了', s(applyOp(mv2, ['b', 'c', 'd', 'e']).list), 'bdce')
const mvBack = { kind: 'move', fileIds: ['b'], target: targetAtGap(L, 4, new Set(['b'])), label: '' }
eq('b 拖到 d、e 之间', s(applyOp(mvBack, L).list), 'acdbe')
eq('放回原处：没有变化', applyOp({ kind: 'move', fileIds: ['b'], target: targetAtGap(L, 1, new Set(['b'])), label: '' }, L).changed, false)

// ---- 移到第 n 页（position） ----
const mvTo2 = { kind: 'move', fileIds: ['e'], target: { position: 1 }, label: '' }
eq('e 移到第 2 页', s(applyOp(mvTo2, L).list), 'aebcd')
eq('e 移到第 2 页，a 被别人移出了', s(applyOp(mvTo2, ['b', 'c', 'd', 'e']).list), 'becd')
eq('移到第 99 页：越界取最后', s(applyOp({ ...mvTo2, fileIds: ['a'], target: { position: 98 } }, L).list), 'bcdea')
eq('移到第 0 页（-1）：越界取最前', s(applyOp({ ...mvTo2, target: { position: -1 } }, L).list), 'eabcd')
eq('要挪的页都不在了：没有变化', applyOp({ ...mvTo2, fileIds: ['Z'] }, L).changed, false)

// ---- 移出 ----
const rm = { kind: 'remove', fileIds: ['b', 'Z', 'd'], label: '' }
const rr = applyOp(rm, L)
eq('移出 b、d（Z 不在）', [s(rr.list), rr.affected, rr.skipped], ['ace', ['b', 'd'], ['Z']])
eq('全部都不在：没有变化', applyOp({ kind: 'remove', fileIds: ['Z'], label: '' }, L).changed, false)

// ---- 插入：已经在条目里的、重复的跳过 ----
const dup = applyOp({ kind: 'insert', files: [N, { ...N }, { ...M, file_id: 'c' }], target: { at: 'end' }, label: '' }, L)
eq('插入时跳过已在的 c 和重复的 N', [s(dup.list), dup.skipped], ['abcdeN', ['N', 'c']])

// ---- resolveTarget 的边界 ----
eq('before 不在、下标越界取最后', resolveTarget({ before: 'Z', index: 99 }, L), 5)
eq('after 不在、下标为负取 0', resolveTarget({ after: 'Z', index: -3 }, L), 0)
eq('start', resolveTarget({ at: 'start' }, L), 0)

console.log(`page-ops：${passed} 条通过${failed ? `，${failed} 条失败` : ''}`)
process.exit(failed ? 1 : 0)
