import { useMemo, useRef, useState } from 'react';
import { api, errMsg } from '../../shared/api';
import { confirmDialog, Dialog, Tip, toast } from '../../shared/ui';
import { refreshRecords } from '../lib/data';
import { sub } from '../lib/records';
import { closeModal } from '../lib/store';
import type { DNSRecord, Listing, RecordsData } from '../types';

const PRESET_TAGS = ['需 VPN', '需帳號', '課程用', '測試中', '社員限定'];

type IconMode = 'auto' | 'si' | 'upload' | 'none';

export function ListingDialog({ record, data }: { record: DNSRecord; data: RecordsData }) {
  const l = record.listing;
  const isNew = !l;
  const defaultTitle = (record.meta.purpose && [...record.meta.purpose].length <= 20 ? record.meta.purpose : sub(record.name, data.zone)) || record.name;
  const [title, setTitle] = useState(l?.title ?? defaultTitle);
  const [description, setDescription] = useState(l?.description ?? '');
  const [category, setCategory] = useState(l?.category ?? '');
  const [sortOrder, setSortOrder] = useState(String(l?.sort_order ?? 0));
  const [url, setUrl] = useState(l?.url ?? '');
  const [tags, setTags] = useState(l?.tags ? l.tags.split(',').join(', ') : '');
  const [featured, setFeatured] = useState(l?.featured ?? false);
  const initialMode: IconMode = !l ? 'auto' : l.icon.startsWith('si:') ? 'si' : (l.icon as IconMode);
  const [mode, setMode] = useState<IconMode>(initialMode);
  const [slug, setSlug] = useState(l?.icon.startsWith('si:') ? l.icon.slice(3) : '');
  const [current, setCurrent] = useState<Listing | null>(l);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const fileRef = useRef<HTMLInputElement>(null);

  const categories = useMemo(() => Array.from(new Set(data.records.map((r) => r.listing?.category).filter(Boolean) as string[])).sort(), [data.records]);
  const usedTags = useMemo(() => {
    const s = new Set<string>(PRESET_TAGS);
    data.records.forEach((r) => r.listing?.tags.split(',').forEach((t) => t.trim() && s.add(t.trim())));
    return Array.from(s);
  }, [data.records]);
  const tagList = tags
    .split(',')
    .map((t) => t.trim())
    .filter(Boolean);

  const addTag = (t: string) => {
    if (tagList.includes(t)) return;
    setTags([...tagList, t].join(', '));
  };

  const iconValue = mode === 'si' ? `si:${slug.trim().toLowerCase()}` : mode;

  const save = async () => {
    setError('');
    if (mode === 'upload' && current?.icon !== 'upload') {
      setError('請先上傳圖片');
      return;
    }
    setBusy(true);
    try {
      const res = await api<{ listing: Listing; icon_error: string }>('PUT', `/api/listings/${record.id}`, {
        title,
        description,
        category,
        url,
        sort_order: Number(sortOrder) || 0,
        featured,
        tags: tagList,
        icon: iconValue,
      });
      if (res.icon_error) toast(res.icon_error, { error: true });
      else toast(isNew ? '已上架' : '已儲存');
      closeModal();
      refreshRecords();
    } catch (e) {
      setError(errMsg(e));
    } finally {
      setBusy(false);
    }
  };

  const upload = async (f: File) => {
    setError('');
    if (isNew) {
      setError('請先儲存上架設定，再上傳圖片');
      return;
    }
    const fd = new FormData();
    fd.append('file', f);
    try {
      const res = await api<{ listing: Listing }>('POST', `/api/listings/${record.id}/icon`, fd);
      setCurrent(res.listing);
      setMode('upload');
      toast('已上傳圖示');
      refreshRecords();
    } catch (e) {
      setError(errMsg(e));
    }
  };

  const refetch = async () => {
    try {
      const res = await api<{ listing: Listing }>('POST', `/api/listings/${record.id}/icon/refresh`);
      setCurrent(res.listing);
      toast('已重新取得圖示');
      refreshRecords();
    } catch (e) {
      toast(errMsg(e), { error: true });
    }
  };

  const remove = async () => {
    const ok = await confirmDialog({ title: '從入口下架', body: <p>「{l?.title}」將不再顯示於服務入口。</p>, confirmText: '下架', danger: true });
    if (!ok) return;
    try {
      await api('DELETE', `/api/listings/${record.id}`);
      toast('已下架');
      closeModal();
      refreshRecords();
    } catch (e) {
      toast(errMsg(e), { error: true });
    }
  };

  const previewURL = mode === 'none' ? '' : mode === 'si' ? (current?.icon === iconValue ? current?.icon_url : '') : mode === initialMode || mode === 'upload' ? current?.icon_url ?? '' : '';

  return (
    <Dialog
      title={isNew ? '上架到服務入口' : '服務入口設定'}
      onClose={closeModal}
      width={640}
      testId="listing-dialog"
      footer={
        <>
          {!isNew && (
            <button type="button" className="btn danger-text" onClick={remove}>
              下架
            </button>
          )}
          {error && <span className="lv-high small foot-error">{error}</span>}
          <span className="spacer" />
          <button type="button" className="btn" onClick={closeModal}>
            取消
          </button>
          <button type="button" className="btn primary" disabled={busy} onClick={save}>
            儲存
          </button>
        </>
      }
    >
      <p className="small muted mono">{record.name}</p>
      <div className="two">
        <div className="field">
          <label htmlFor="ls-title">顯示名稱</label>
          <input id="ls-title" className="input" value={title} maxLength={40} onChange={(e) => setTitle(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="ls-cat">分類</label>
          <input id="ls-cat" className="input" list="ls-cats" value={category} placeholder="例：監控" maxLength={20} onChange={(e) => setCategory(e.target.value)} />
          <datalist id="ls-cats">
            {categories.map((c) => (
              <option key={c} value={c} />
            ))}
          </datalist>
        </div>
      </div>
      <div className="field">
        <label htmlFor="ls-desc">說明</label>
        <textarea id="ls-desc" className="input" value={description} maxLength={200} onChange={(e) => setDescription(e.target.value)} />
      </div>
      <div className="two">
        <div className="field">
          <label htmlFor="ls-url">
            網址 <Tip text="留空則使用 https://名稱；需指定路徑或連接埠時填寫" />
          </label>
          <input id="ls-url" className="input mono" value={url} placeholder={`https://${record.name}`} onChange={(e) => setUrl(e.target.value)} />
        </div>
        <div className="field">
          <label htmlFor="ls-sort">
            排序 <Tip text="數字小的排在前面。" />
          </label>
          <input id="ls-sort" className="input" type="number" value={sortOrder} onChange={(e) => setSortOrder(e.target.value)} />
        </div>
      </div>
      <div className="field">
        <label htmlFor="ls-tags">標籤</label>
        <input id="ls-tags" className="input" value={tags} placeholder="逗號分隔，最多 6 個" onChange={(e) => setTags(e.target.value)} />
        <div className="tag-adds small">
          {usedTags
            .filter((t) => !tagList.includes(t))
            .map((t) => (
              <button key={t} type="button" className="linkbtn" onClick={() => addTag(t)}>
                + {t}
              </button>
            ))}
        </div>
      </div>
      <div className="field">
        <span className="field-label">圖示</span>
        <div className="icon-pick">
          <div className="icon-preview" aria-hidden>
            {previewURL ? <img src={previewURL} alt="" /> : <span>{[...title][0] ?? '?'}</span>}
          </div>
          <div className="icon-modes">
            {(
              [
                ['auto', '自動取得'],
                ['si', 'Simple Icons'],
                ['upload', '上傳圖片'],
                ['none', '不使用'],
              ] as const
            ).map(([k, label]) => (
              <label key={k} className="check">
                <input type="radio" name="icon-mode" checked={mode === k} onChange={() => setMode(k)} />
                {label}
              </label>
            ))}
            {mode === 'auto' && !isNew && (
              <button type="button" className="btn small" onClick={refetch}>
                重新取得
              </button>
            )}
            {mode === 'si' && (
              <input className="input mono" value={slug} placeholder="例：gitlab" aria-label="Simple Icons 名稱" onChange={(e) => setSlug(e.target.value)} />
            )}
            {mode === 'upload' && (
              <>
                <input ref={fileRef} type="file" accept="image/*,.ico,.svg" hidden onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
                <button type="button" className="btn small" onClick={() => fileRef.current?.click()}>
                  選擇檔案
                </button>
                <span className="small muted">PNG、JPEG、GIF、WebP、ICO、SVG，上限 1 MB</span>
              </>
            )}
          </div>
        </div>
      </div>
      <label className="check">
        <input type="checkbox" checked={featured} onChange={(e) => setFeatured(e.target.checked)} />
        精選
      </label>
    </Dialog>
  );
}
