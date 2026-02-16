import { useState, useEffect, useRef, useCallback } from 'react';
import { SelectEpub, ConvertBook, GetLogs } from '../wailsjs/go/main/App';
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
  const terminalRef = useRef<HTMLDivElement>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // ── 自动滚动（使用 rAF 确保流畅）──────────────────────────────────
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

  // ── 日志轮询（转换时启动，完成时停止）─────────────────────────────
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
        // 忽略轮询错误
      }
    }, 150);

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
      // 1. 选择文件
      const filePath = await SelectEpub();
      if (!filePath) return;

      setIsConverting(true);
      setProgress(0);
      setLogs(['🚀 任务启动...']);

      // 2. 执行转换（dual output）
      const result = (await ConvertBook(filePath, 'both')) as ConversionResult;

      // 3. 最终拉取日志
      const finalLogs = await GetLogs();
      if (finalLogs && finalLogs.length > 0) {
        setLogs(finalLogs);
      }

      // 4. 显示结果
      if (result.isError) {
        alert(`❌ 转换失败:\n${result.message}`);
      } else {
        const parts: string[] = ['✅ 转换完成！\n'];
        if (result.pdfPath) parts.push(`📄 PDF: ${result.pdfPath}`);
        if (result.markdownPath) parts.push(`📝 Markdown: ${result.markdownPath}`);
        alert(parts.join('\n'));
      }
    } catch (err) {
      alert(`💥 未知错误: ${err}`);
    } finally {
      setIsConverting(false);
      setProgress(100);
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

        {isConverting && (
          <div className="progress-bar">
            <div className="progress-fill" style={{ width: `${progress}%` }} />
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

// ── 单行日志组件（避免 dangerouslySetInnerHTML 的 XSS 风险）─────────
function LogLine({ text }: { text: string }) {
  if (!text) return null;

  // 根据内容类型设置样式
  let className = 'log-line';
  if (text.includes('❌')) className += ' log-error';
  else if (text.includes('✅')) className += ' log-success';
  else if (text.includes('⚠️')) className += ' log-warn';
  else if (text.includes('🧼')) className += ' log-sanitize';
  else if (text.includes('🔧')) className += ' log-repair';

  return <div className={className}>{text}</div>;
}

export default App;