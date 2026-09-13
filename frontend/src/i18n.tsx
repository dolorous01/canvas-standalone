import { createContext, useContext, type ReactNode } from 'react'

const dictionaries = {
  'zh-CN': {
    projects: '项目', newProject: '新建项目', untitled: '未命名画布', apiKey: 'API Key', model: '模型',
    createKey: '创建 API Key', prompt: '提示词', generate: '生成', edit: '编辑', generating: '生成中',
    addText: '添加文本', addImage: '上传图片', addGroup: '添加分组', addConfig: '添加配置', undo: '撤销', redo: '重做',
    zoomIn: '放大', zoomOut: '缩小', fit: '适应视图', import: '导入', export: '导出', save: '保存', saved: '已保存',
    saving: '保存中', conflict: '版本冲突', failed: '失败', partial: '部分完成', cancel: '取消', source: '关于与源码',
    sourceTitle: 'Infinite Canvas 来源', close: '关闭', delete: '删除', generation: '文生图', imageEdit: '图片编辑',
    noProject: '暂无画布项目', selectImage: '选择一个图片节点进行编辑', emptyKey: '需要一个可用的 API Key',
    emptyModel: '当前 Key 没有可用图片模型', policyFallback: '模型服务异常时会按管理员顺序回退', reload: '重新加载',
    saveCopy: '另存为新项目', completed: '已完成', canceled: '已取消', collapseProjects: '收起项目列表',
    showProjects: '显示项目列表', projectName: '项目名称', connect: '连接所选节点', operation: '生成模式', size: '尺寸',
    preset: '预设', defaultPreset: '模型默认', experimental: '实验性', customSize: '自定义', width: '宽', height: '高',
    aspectRatio: '比例', resolution: '分辨率', quality: '质量', format: '格式', background: '背景', compression: '压缩质量',
    outputCount: '数量', textPlaceholder: '文本', groupDefault: '分组', nodeCancel: '取消任务', invalidDocument: '无效的画布文档',
    nodeImage: '图片', nodeText: '文本', nodeConfig: '配置', nodeGroup: '分组', upstream: '上游项目', commit: '提交',
    license: '许可证', modified: '修改日期', warranty: '本程序不提供任何担保。Sub2API 通过已认证的服务端处理模型访问、持久化和图片任务。',
    correspondingSource: '对应源码', conflictActions: '服务器版本已变化，请重新加载或另存本地草稿。',
    searchProjects: '搜索项目', noProjectResults: '没有匹配的项目', duplicateProject: '复制项目', deleteProject: '删除项目',
    deleteProjectTitle: '删除项目', deleteProjectBody: '此操作将永久删除该画布项目，且无法撤销。',
    copySuffix: '副本', nodes: '个节点', runningJobs: '个任务', closeProjects: '关闭项目列表', imageStudio: '图片创作',
    showInspector: '打开生成面板', hideInspector: '收起生成面板', canvasTools: '画布工具', selectedCount: '已选',
    editSelected: '编辑所选图片', generationSettings: '生成参数', projectActions: '项目操作'
  },
  en: {
    projects: 'Projects', newProject: 'New project', untitled: 'Untitled canvas', apiKey: 'API Key', model: 'Model',
    createKey: 'Create API key', prompt: 'Prompt', generate: 'Generate', edit: 'Edit', generating: 'Generating',
    addText: 'Add text', addImage: 'Upload image', addGroup: 'Add group', addConfig: 'Add config', undo: 'Undo', redo: 'Redo',
    zoomIn: 'Zoom in', zoomOut: 'Zoom out', fit: 'Fit view', import: 'Import', export: 'Export', save: 'Save', saved: 'Saved',
    saving: 'Saving', conflict: 'Version conflict', failed: 'Failed', partial: 'Partial', cancel: 'Cancel', source: 'About and source',
    sourceTitle: 'Infinite Canvas source', close: 'Close', delete: 'Delete', generation: 'Generate', imageEdit: 'Edit image',
    noProject: 'No canvas projects', selectImage: 'Select an image node to edit', emptyKey: 'A usable API key is required',
    emptyModel: 'No image model is available for this key', policyFallback: 'Service failures follow the administrator fallback order', reload: 'Reload',
    saveCopy: 'Save as new project', completed: 'Completed', canceled: 'Canceled', collapseProjects: 'Collapse projects',
    showProjects: 'Show projects', projectName: 'Project name', connect: 'Connect selected nodes', operation: 'Generation mode', size: 'Size',
    preset: 'Preset', defaultPreset: 'Model default', experimental: 'Experimental', customSize: 'Custom', width: 'Width', height: 'Height',
    aspectRatio: 'Ratio', resolution: 'Resolution', quality: 'Quality', format: 'Format', background: 'Background', compression: 'Compression',
    outputCount: 'Count', textPlaceholder: 'Text', groupDefault: 'Group', nodeCancel: 'Cancel job', invalidDocument: 'Invalid canvas document',
    nodeImage: 'Image', nodeText: 'Text', nodeConfig: 'Config', nodeGroup: 'Group', upstream: 'Upstream', commit: 'Commit',
    license: 'License', modified: 'Modified', warranty: 'This program is provided without warranty. Sub2API routes provider access, persistence, and image jobs through its authenticated server.',
    correspondingSource: 'Corresponding source', conflictActions: 'The server version changed. Reload it or save the local draft as a new project.',
    searchProjects: 'Search projects', noProjectResults: 'No matching projects', duplicateProject: 'Duplicate project', deleteProject: 'Delete project',
    deleteProjectTitle: 'Delete project', deleteProjectBody: 'This permanently deletes the canvas project and cannot be undone.',
    copySuffix: 'copy', nodes: 'nodes', runningJobs: 'jobs', closeProjects: 'Close projects', imageStudio: 'Image studio',
    showInspector: 'Open generation panel', hideInspector: 'Close generation panel', canvasTools: 'Canvas tools', selectedCount: 'selected',
    editSelected: 'Edit selected image', generationSettings: 'Generation settings', projectActions: 'Project actions'
  }
} as const

type Dictionary = Record<keyof typeof dictionaries.en, string>
type TranslationKey = keyof Dictionary
const I18nContext = createContext<Dictionary>(dictionaries.en)

export function CanvasI18nProvider({ locale, children }: { locale: string; children: ReactNode }) {
  const dictionary = locale.toLowerCase().startsWith('zh') ? dictionaries['zh-CN'] : dictionaries.en
  return <I18nContext.Provider value={dictionary}>{children}</I18nContext.Provider>
}

export function useCanvasI18n() {
  const dictionary = useContext(I18nContext)
  return (key: TranslationKey) => dictionary[key]
}

export { dictionaries }
