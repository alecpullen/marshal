<script lang="ts">
  let { diff }: { diff: string } = $props()

  type Kind = 'add' | 'del' | 'hunk' | 'meta' | 'ctx'
  function kindOf(line: string): Kind {
    if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff ') || line.startsWith('index ')) return 'meta'
    if (line.startsWith('@@')) return 'hunk'
    if (line.startsWith('+')) return 'add'
    if (line.startsWith('-')) return 'del'
    return 'ctx'
  }
  const lines = $derived(diff.split('\n').map((text) => ({ text, kind: kindOf(text) })))
  const cls: Record<Kind, string> = {
    add: 'bg-ok/10 text-ok',
    del: 'bg-err/10 text-err',
    hunk: 'text-info',
    meta: 'text-muted',
    ctx: 'text-sub',
  }
</script>

<pre class="overflow-x-auto rounded bg-bg p-2 font-mono text-xs" data-testid="diff-lines">{#each lines as l, i (i)}<span class="block whitespace-pre {cls[l.kind]}" data-kind={l.kind}>{l.text || ' '}</span>{/each}</pre>
