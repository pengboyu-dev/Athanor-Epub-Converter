import { useState, useEffect, useRef, useCallback } from 'react';
import { SelectEpub, ConvertBook, GetLogsSince } from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';
import './App.css';

// ── Types ──────────────────────────────────────────────────────────

interface ConversionResult {
  jobId: string;
  stage: string;
  progress: number;
  message: string;
  isComplete: boolean;
  isError: boolean;
  outputPath?: string;
  markdownPath?: string;
}

interface LogLineEvent {
  seq: number;
  line: string;
}

interface LogsSinceResult {
  lines: string[];
  nextSeq: number;
}

// ── Component ──────────────────────────────────────────────────────

function App() {
  const [logs, setLogs] = useState<string[]>([]);
  const [isConverting, setIsConverting] = useState(false);
  const [progress, setProgress] = useState(0);
  const [statusMsg, setStatusMsg] = useState('');
  const terminalRef = useRef<HTMLDivElement>(null);

  // Sequence number tracking for incremental log delivery.
  // We use a ref so the event callback always sees the latest value
  // without needing to be in the useEffect dependency array.
  const nextSeqRef = useRef(0);

  // ── Auto-scroll terminal ─────────────────────────────────────────
  useEffect(() => {
    if (terminalRef.current) {
      requestAnimationFrame(() => {
        const el = terminalRef.current;
        if (el) {
          el.scrollTop = el.scrollHeight;
        }
      });
    }
  }, [logs]);

  // ── Fetch full log history on mount (backfill) ───────────────────
  useEffect(() => {
    (async () => {
      try {
        const result = (await GetLogsSince(0)) as LogsSinceResult;
        if (result && result.lines && result.lines.length > 0) {
          setLogs(result.lines);
          nextSeqRef.current = result.nextSeq;
        }
      } catch {
        // Backend may not be ready yet — ignore.
      }
    })();
  }, []);

  // ── Subscribe to incremental log events ──────────────────────────
  useEffect(() => {
    const cancel = EventsOn('log:line', (data: LogLineEvent) => {
      if (!data || typeof data.line !== 'string') return;

      // If the incoming seq matches what we expect, just append.
      // If there is a gap (e.g. we missed events), we will do a
      // backfill on the next convert cycle. For normal operation
      // the events arrive in order and this is sufficient.
      setLogs((prev) => [...prev, data.line]);
      nextSeqRef.current = data.seq + 1;
    });

    return () => {
      if (typeof cancel === 'function') cancel();
    };
  }, []);

  // ── Subscribe to conversion progress events ─────────────────────
  useEffect(() => {
    const cancel = EventsOn('conversion:progress', (data: ConversionResult) => {
      if (data && data.progress !== undefined) {
        setProgress(data.progress);
      }
      if (data && data.message) {
        setStatusMsg(data.message);
      }
    });

    return () => {
      if (typeof cancel === 'function') cancel();
    };
  }, []);

  // ── Convert handler ──────────────────────────────────────────────
  const handleConvert = useCallback(async () => {
    try {
      const filePath = await SelectEpub();
      if (!filePath) return;

      setIsConverting(true);
      setProgress(0);
      setStatusMsg('🚀 任务启动...');

      // Backfill any logs we may have missed, then clear and start fresh.
      try {
        const backfill = (await GetLogsSince(nextSeqRef.current)) as LogsSinceResult;
        if (backfill && backfill.lines && backfill.lines.length > 0) {
          setLogs((prev) => [...prev, ...backfill.lines]);
          nextSeqRef.current = backfill.nextSeq;
        }
      } catch {
        // Non-critical.
      }

      const result = (await ConvertBook(filePath, 'rag-md')) as ConversionResult;

      // Final backfill to make sure we have every log line.
      try {
        const final = (await GetLogsSince(nextSeqRef.current)) as LogsSinceResult;
        if (final && final.lines && final.lines.length > 0) {
          setLogs((prev) => [...prev, ...final.lines]);
          nextSeqRef.current = final.nextSeq;
        }
      } catch {
        // Non-critical.
      }

      if (result.isError) {
        setProgress(0);
        setStatusMsg('❌ ' + result.message);
        alert(`❌ 转换失败:\n${result.message}`);
      } else {
        setProgress(100);
        setStatusMsg('✅ 转换完成');
        const parts: string[] = ['✅ 转换完成！\n'];
        if (result.markdownPath) parts.push(`📝 Markdown: ${result.markdownPath}`);
        alert(parts.join('\n'));
      }
    } catch (err) {
      setStatusMsg('💥 错误');
      alert(`💥 未知错误: ${err}`);
    } finally {
      setIsConverting(false);
    }
  }, []);

  return (
    <div className="app">
      <header className="app-header">
        <h1>🔥 ATHANOR</h1>
        <p className="subtitle">
          EPUB → RAG 高质量 Markdown
        </p>
      </header>

      <div className="controls">
        <button
          onClick={handleConvert}
          disabled={isConverting}
          className="convert-btn"
        >
          {isConverting ? '🧱 转换中...' : '📚 选择 EPUB 文件'}
        </button>

        {(isConverting || progress > 0) && (
          <div className="progress-section">
            <div className="progress-bar">
              <div
                className="progress-fill"
                style={{ width: `${progress}%` }}
              />
            </div>
            <div className="progress-text">
              <span>{Math.round(progress)}%</span>
              <span className="status-msg">{statusMsg}</span>
            </div>
          </div>
        )}
      </div>

      <div className="terminal" ref={terminalRef}>
        {logs.map((log, i) => (
          <LogLine key={i} text={log} />
        ))}
        {isConverting && <span className="cursor">▋</span>}
      </div>
    </div>
  );
}

// ── Log line component ─────────────────────────────────────────────

function LogLine({ text }: { text: string }) {
  if (!text) return null;

  let className = 'log-line';
  if (text.includes('❌')) className += ' log-error';
  else if (text.includes('✅')) className += ' log-success';
  else if (text.includes('⚠️')) className += ' log-warn';
  else if (text.includes('🧼')) className += ' log-sanitize';
  else if (text.includes('🔧')) className += ' log-repair';
  else if (text.includes('📄 渲染中')) className += ' log-progress';

  return <div className={className}>{text}</div>;
}

export default App;
