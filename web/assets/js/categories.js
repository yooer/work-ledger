/* 分类管理树：记住每个节点的折叠状态（仅本机浏览器），支持全部展开 / 全部折叠 */
(function () {
    'use strict';
    var KEY = 'wl-category-tree-closed';
    var nodes = Array.prototype.slice.call(document.querySelectorAll('details[data-tree-key]'));
    var closed = {};
    try { closed = JSON.parse(window.localStorage.getItem(KEY) || '{}') || {}; } catch (e) { closed = {}; }
    function save() {
        try { window.localStorage.setItem(KEY, JSON.stringify(closed)); } catch (e) { /* 浏览器禁止存储时忽略 */ }
    }
    nodes.forEach(function (d) {
        if (closed[d.dataset.treeKey]) d.open = false;
        d.addEventListener('toggle', function () {
            if (d.open) delete closed[d.dataset.treeKey]; else closed[d.dataset.treeKey] = 1;
            save();
        });
    });
    document.querySelectorAll('[data-tree-all]').forEach(function (btn) {
        btn.addEventListener('click', function () {
            var open = btn.dataset.treeAll === 'open';
            nodes.forEach(function (d) { d.open = open; });
        });
    });
})();
