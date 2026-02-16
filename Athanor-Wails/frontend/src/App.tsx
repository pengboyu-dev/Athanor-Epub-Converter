import { useState, useEffect, useRef, useCallback } from 'react';
import { SelectEpub, ConvertBook, GetLogs } from '../wailsjs/go/main/App';
import { EventsOn, EventsOff } from '../wailsjs/runtime/runtime';
import './App.css';

interface ConversionResult {
  jobId: string;
  stage: string;
  progress: number;
  message: string;
  isComplete: boolean;
  isError: boolean;
  outputPath?: string;
  pdfPath?: string;
  markdownPath?: string;
}

function App() {
  const [logs, setLogs] = useState<string[]>([]);
  const [isConverting, setIsConverting] = useState(false);
  const [progress, setProgress] = useState(0);
  const [statusMsg, setStatusMsg] = useState('');
  const terminalRef = useRef<HTMLDivElement>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // ── 自动滚动 ─────────────────────────────────────────────────────
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

  // ── 监听后端进度事件 ─────────────────────────────────────────────
  useEffect(() => {
    const cancelProgress = EventsOn('conversion:progress', (data: ConversionResult) => {
      if (data && data.progress !== undefined) {
        setProgress(data.progress);
      }
      if (data && data.message) {
        setStatusMsg(data.message);
      }
    });

    return () => {
      if (typeof cancelProgress === 'function') cancelProgress();
      EventsOff('conversion:progress');
    };
  }, []);

  // ── 日志轮询 ─────────────────────────────────────────────────────
  useEffect(() => {
    if (!isConverting) {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
      return;
    }

    pollRef.current = setInterval(async () => {
      try {
        const newLogs = await GetLogs();
        if (newLogs && newLogs.length > 0) {
          setLogs(newLogs);
        }
      } catch {
        // 忽略
      }
    }, 200);

    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [isConverting]);

  // ── 转换处理 ─────────────────────────────────────────────────────
  const handleConvert = useCallback(async () => {
    try {
      const filePath = await SelectEpub();
      if (!filePath) return;

      setIsConverting(true);
      setProgress(0);
      setStatusMsg('🚀 任务启动...');
      setLogs(['🚀 任务启动...']);

      const result = (await ConvertBook(filePath, 'both')) as ConversionResult;

      const finalLogs = await GetLogs();
      if (finalLogs && finalLogs.length > 0) {
        setLogs(finalLogs);
      }

      if (result.isError) {
        setProgress(0);
        setStatusMsg('❌ ' + result.message);
        alert(`❌ 转换失败:\n${result.message}`);
      } else {
        setProgress(100);
        setStatusMsg('✅ 转换完成');
        const parts: string[] = ['✅ 转换完成！\n'];
        if (result.pdfPath) parts.push(`📄 PDF: ${result.pdfPath}`);
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
          EPUB → PDF (人类阅读) + Markdown (AI 阅读)
        </p>
      </header>

      <div className="controls">
        <button
          onClick={handleConvert}
          disabled={isConverting}
          className="convert-btn"
        >
          {isConverting ? '🧼 处理中...' : '📚 选择 EPUB 文件'}
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