/* 工作台账：公共前端脚本（Ajax 调用、通用表单提交、提示、组件初始化） */
(function () {
    'use strict';

    const $ = (s, root) => (root || document).querySelector(s);
    const $$ = (s, root) => Array.from((root || document).querySelectorAll(s));
    const csrf = () => (document.querySelector('meta[name="csrf-token"]') || {}).content || '';

    const escapeHtml = (s) => String(s == null ? '' : s)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');

    /* ---------------- 提示 ---------------- */
    function toast(type, message) {
        if (!message) return;
        const box = document.getElementById('wl-toasts');
        if (!box) { window.alert(message); return; }
        const cls = { success: 'success', error: 'danger', warning: 'warning', info: 'info' }[type] || 'info';
        const el = document.createElement('div');
        el.className = 'alert alert-' + cls + ' alert-dismissible wl-toast shadow-sm';
        el.setAttribute('role', 'alert');
        el.innerHTML = '<div>' + escapeHtml(message) + '</div><a class="btn-close" aria-label="关闭"></a>';
        el.querySelector('.btn-close').addEventListener('click', () => el.remove());
        box.appendChild(el);
        window.setTimeout(() => el.remove(), type === 'success' ? 4000 : 8000);
    }

    /* ---------------- Ajax：POST + JSON + action ---------------- */
    async function api(url, action, data) {
        const body = Object.assign({}, data || {}, { action: action });
        let res;
        try {
            res = await fetch(url, {
                method: 'POST',
                credentials: 'same-origin',
                headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf(), 'Accept': 'application/json' },
                body: JSON.stringify(body),
            });
        } catch (e) {
            return { ok: false, message: '网络异常，请检查连接后重试。' };
        }
        return handleResponse(res);
    }

    async function upload(url, action, formData) {
        formData.set('action', action);
        formData.set('_csrf', csrf());
        let res;
        try {
            res = await fetch(url, { method: 'POST', credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf(), 'Accept': 'application/json' }, body: formData });
        } catch (e) {
            return { ok: false, message: '网络异常，请检查连接后重试。' };
        }
        return handleResponse(res);
    }

    async function handleResponse(res) {
        let json;
        try { json = await res.json(); } catch (e) { json = { ok: false, message: '服务器返回了无法识别的内容（HTTP ' + res.status + '）。' }; }
        if (res.status === 401 && json.redirect) {
            window.location.href = json.redirect;
        }
        return json;
    }

    /* ---------------- 表单序列化 ---------------- */
    function serialize(form) {
        const data = {};
        Array.from(form.elements).forEach((el) => {
            if (!['INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName)) return;
            if (!el.name || el.disabled || el.type === 'file' || el.name === '_csrf') return;
            const isArray = el.name.endsWith('[]');
            const key = isArray ? el.name.slice(0, -2) : el.name;
            if (el.type === 'checkbox') {
                if (isArray) {
                    data[key] = data[key] || [];
                    if (el.checked) data[key].push(el.value);
                } else {
                    data[key] = el.checked;
                }
                return;
            }
            if (el.type === 'radio') {
                if (el.checked) data[key] = el.value;
                return;
            }
            if (el.tagName === 'SELECT' && el.multiple) {
                data[key] = Array.from(el.selectedOptions).map((o) => o.value);
                return;
            }
            if (isArray) {
                data[key] = data[key] || [];
                data[key].push(el.value);
                return;
            }
            data[key] = el.value;
        });
        return data;
    }

    function clearErrors(form) {
        Array.from(form.elements).forEach((el) => el.classList && el.classList.remove('is-invalid'));
        $$('.wl-field-error').forEach((el) => el.remove());
    }

    function showErrors(form, errors) {
        let first = null;
        Object.keys(errors || {}).forEach((key) => {
            const el = Array.from(form.elements).find((x) => x.name === key || x.name === key + '[]');
            const target = el ? (el.closest('[data-field]') || el) : null;
            if (!target) return;
            if (el) el.classList.add('is-invalid');
            const msg = document.createElement('div');
            msg.className = 'invalid-feedback d-block wl-field-error';
            msg.textContent = errors[key];
            (target === el ? el.parentNode : target).appendChild(msg);
            if (!first) first = el;
        });
        if (first && first.focus) first.focus();
    }

    function setBusy(button, busy) {
        if (!button) return;
        if (busy) {
            button.dataset.label = button.innerHTML;
            button.disabled = true;
            button.classList.add('btn-loading');
        } else {
            button.disabled = false;
            button.classList.remove('btn-loading');
        }
    }

    function afterSuccess(json, form) {
        if (json.redirect) { window.location.href = json.redirect; return; }
        if (json.reload) { window.location.reload(); return; }
        if (json.message) toast('success', json.message);
        if (form && form.hasAttribute('data-reset')) form.reset();
    }

    /* 通用 Ajax 表单：<form data-ajax data-url="/xxx/ajax" data-action="create"> */
    document.addEventListener('submit', async (event) => {
        const form = event.target;
        if (!(form instanceof HTMLFormElement) || !form.hasAttribute('data-ajax')) return;
        event.preventDefault();
        if (form.dataset.confirm && !window.confirm(form.dataset.confirm)) return;
        const submitter = event.submitter || form.querySelector('[type="submit"]');
        const action = (submitter && submitter.dataset.action) || form.dataset.action;
        clearErrors(form);
        setBusy(submitter, true);
        let json;
        if (form.hasAttribute('data-multipart')) {
            json = await upload(form.dataset.url, action, new FormData(form));
        } else {
            json = await api(form.dataset.url, action, serialize(form));
        }
        setBusy(submitter, false);
        form.dispatchEvent(new CustomEvent('wl:response', { detail: json }));
        if (json.ok) {
            if (!form.hasAttribute('data-custom-success')) afterSuccess(json, form);
        } else {
            showErrors(form, json.errors);
            toast('error', json.message || '操作失败');
        }
    });

    /* 通用 Ajax 按钮：<button data-ajax-click data-url data-action data-payload='{"id":1}' data-confirm> */
    document.addEventListener('click', async (event) => {
        const btn = event.target.closest('[data-ajax-click]');
        if (!btn) return;
        event.preventDefault();
        if (btn.dataset.confirm && !window.confirm(btn.dataset.confirm)) return;
        let payload = {};
        try { payload = btn.dataset.payload ? JSON.parse(btn.dataset.payload) : {}; } catch (e) { payload = {}; }
        setBusy(btn, true);
        const json = await api(btn.dataset.url, btn.dataset.action, payload);
        setBusy(btn, false);
        btn.dispatchEvent(new CustomEvent('wl:response', { detail: json, bubbles: true }));
        if (json.ok) {
            if (!btn.hasAttribute('data-custom-success')) afterSuccess(json, null);
        } else {
            toast('error', json.message || '操作失败');
        }
    });

    /* 退出登录 */
    $$('[data-logout]').forEach((a) => a.addEventListener('click', async (e) => {
        e.preventDefault();
        const json = await api('/auth/ajax', 'logout', {});
        window.location.href = (json && json.redirect) || '/login';
    }));

    /* 成功提示自动消失 */
    window.setTimeout(() => $$('.wl-autohide').forEach((el) => el.remove()), 7000);

    /* 密码显示/隐藏：<button data-password-toggle="inputId"> */
    $$('[data-password-toggle]').forEach((button) => {
        button.addEventListener('click', (e) => {
            e.preventDefault();
            const input = document.getElementById(button.dataset.passwordToggle || '');
            if (!input) return;
            const show = input.type === 'password';
            input.type = show ? 'text' : 'password';
            button.textContent = show ? '隐藏' : '显示';
        });
    });

    /* 字数统计：<textarea data-count-target="#id"> */
    $$('[data-count-target]').forEach((ta) => {
        const counter = $(ta.dataset.countTarget);
        if (!counter) return;
        const update = () => { counter.textContent = String(Array.from(ta.value).length); };
        ta.addEventListener('input', update);
        update();
    });

    /* 可搜索下拉（tom-select）：<select data-tom> */
    function initTom(root) {
        if (typeof window.TomSelect === 'undefined') return;
        $$('select[data-tom]', root).forEach((el) => {
            if (el.tomselect) return;
            // Tabler 附带的是 tom-select 精简版（不含插件），多选项可用退格键删除。
            const opts = { allowEmptyOption: true, maxOptions: 500, hidePlaceholder: false };
            if (el.dataset.placeholder) opts.placeholder = el.dataset.placeholder;
            new window.TomSelect(el, opts);
        });
    }

    /* 日期选择（litepicker）：<input data-datepicker> */
    function initDatepicker(root) {
        if (typeof window.Litepicker === 'undefined') return;
        $$('input[data-datepicker]', root).forEach((el) => {
            if (el.dataset.pickerReady) return;
            el.dataset.pickerReady = '1';
            el.setAttribute('autocomplete', 'off');
            new window.Litepicker({
                element: el,
                format: 'YYYY-MM-DD',
                lang: 'zh-CN',
                firstDay: 1,
                autoApply: true,
                dropdowns: { minYear: 2000, maxYear: 2100, months: true, years: true },
                buttonText: { previousMonth: '‹', nextMonth: '›', reset: '清空', apply: '确定', cancel: '取消' },
                setup: (picker) => picker.on('selected', () => el.dispatchEvent(new Event('change', { bubbles: true }))),
            });
        });
    }

    document.addEventListener('DOMContentLoaded', () => { initTom(document); initDatepicker(document); });
    if (document.readyState !== 'loading') { initTom(document); initDatepicker(document); }

    window.WL = { api, upload, toast, serialize, escapeHtml, initTom, initDatepicker, showErrors, clearErrors };
})();
