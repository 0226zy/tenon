import { FormEvent, useEffect, useState } from 'react';

type Item = { id: number; title: string; created_at: string };

export default function App() {
  const [items, setItems] = useState<Item[]>([]);
  const [title, setTitle] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  async function load() {
    try {
      const response = await fetch('/api/v1/items');
      if (!response.ok) throw new Error('加载失败');
      const data = (await response.json()) as { items: Item[] };
      setItems(data.items);
      setError('');
    } catch {
      setError('暂时无法连接后端，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { void load(); }, []);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const value = title.trim();
    if (!value) return;
    const response = await fetch('/api/v1/items', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ title: value }),
    });
    if (!response.ok) {
      setError('保存失败，请检查输入后重试。');
      return;
    }
    setTitle('');
    await load();
  }

  async function remove(id: number) {
    const response = await fetch(`/api/v1/items/${id}`, { method: 'DELETE' });
    if (response.ok) setItems((current) => current.filter((item) => item.id !== id));
  }

  return (
    <main className="shell">
      <section className="hero">
        <span className="eyebrow">REACT · GO · SQLITE</span>
        <h1>把下一个想法，变成可验证的原型。</h1>
        <p>一个简洁的全栈起点：前端、API 与持久化已经连通。</p>
      </section>
      <section className="panel" aria-labelledby="items-title">
        <div className="panel-heading">
          <div>
            <span className="section-kicker">工作清单</span>
            <h2 id="items-title">验证核心数据链路</h2>
          </div>
          <span className="count">{items.length} 条</span>
        </div>
        <form onSubmit={submit} className="composer">
          <input
            aria-label="新条目"
            value={title}
            onChange={(event) => setTitle(event.target.value)}
            placeholder="输入一条要验证的想法…"
            maxLength={200}
          />
          <button type="submit" disabled={!title.trim()}>添加</button>
        </form>
        {error && <p role="alert" className="error">{error}</p>}
        <div className="items">
          {loading ? <p className="empty">正在加载…</p> : items.length === 0 ? (
            <p className="empty">还没有条目。添加第一条，确认 SQLite 写入与读取。</p>
          ) : items.map((item) => (
            <article className="item" key={item.id}>
              <div>
                <strong>{item.title}</strong>
                <time>{new Date(item.created_at).toLocaleString()}</time>
              </div>
              <button className="delete" onClick={() => void remove(item.id)} aria-label={`删除 ${item.title}`}>删除</button>
            </article>
          ))}
        </div>
      </section>
    </main>
  );
}
