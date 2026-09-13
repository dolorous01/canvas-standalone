export interface OutpaintGeometry {
  width: number
  height: number
  sourceX: number
  sourceY: number
  sourceWidth: number
  sourceHeight: number
}

export interface OutpaintFiles extends OutpaintGeometry {
  source: File
  mask: File
}

const maxCanvasEdge = 16_384
const maxCanvasPixels = 67_108_864

export function outpaintGeometry(width: number, height: number, targetRatio: number): OutpaintGeometry {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0 ||
    !Number.isFinite(targetRatio) || targetRatio <= 0) {
    throw new Error('Invalid outpaint dimensions')
  }
  const sourceWidth = Math.round(width)
  const sourceHeight = Math.round(height)
  const sourceRatio = sourceWidth / sourceHeight
  const outputWidth = sourceRatio < targetRatio ? Math.ceil(sourceHeight * targetRatio) : sourceWidth
  const outputHeight = sourceRatio > targetRatio ? Math.ceil(sourceWidth / targetRatio) : sourceHeight
  if (outputWidth > maxCanvasEdge || outputHeight > maxCanvasEdge || outputWidth * outputHeight > maxCanvasPixels) {
    throw new Error('Outpaint canvas exceeds the supported size')
  }
  return {
    width: outputWidth,
    height: outputHeight,
    sourceX: Math.floor((outputWidth - sourceWidth) / 2),
    sourceY: Math.floor((outputHeight - sourceHeight) / 2),
    sourceWidth,
    sourceHeight
  }
}

export async function createOutpaintFiles(imageURL: string, targetRatio: number): Promise<OutpaintFiles> {
  const image = await loadImage(imageURL)
  const geometry = outpaintGeometry(image.naturalWidth || image.width, image.naturalHeight || image.height, targetRatio)
  if (geometry.width === geometry.sourceWidth && geometry.height === geometry.sourceHeight) {
    throw new Error('Image already matches the selected aspect ratio')
  }
  const sourceCanvas = document.createElement('canvas')
  sourceCanvas.width = geometry.width
  sourceCanvas.height = geometry.height
  const sourceContext = sourceCanvas.getContext('2d')
  if (!sourceContext) throw new Error('Image canvas is unavailable')
  sourceContext.clearRect(0, 0, geometry.width, geometry.height)
  sourceContext.drawImage(
    image,
    geometry.sourceX,
    geometry.sourceY,
    geometry.sourceWidth,
    geometry.sourceHeight
  )

  const maskCanvas = document.createElement('canvas')
  maskCanvas.width = geometry.width
  maskCanvas.height = geometry.height
  const maskContext = maskCanvas.getContext('2d')
  if (!maskContext) throw new Error('Mask canvas is unavailable')
  maskContext.clearRect(0, 0, geometry.width, geometry.height)
  maskContext.fillStyle = '#fff'
  maskContext.fillRect(
    geometry.sourceX,
    geometry.sourceY,
    geometry.sourceWidth,
    geometry.sourceHeight
  )

  const [sourceBlob, maskBlob] = await Promise.all([
    canvasBlob(sourceCanvas),
    canvasBlob(maskCanvas)
  ])
  return {
    ...geometry,
    source: new File([sourceBlob], 'outpaint-source.png', { type: 'image/png' }),
    mask: new File([maskBlob], 'outpaint-mask.png', { type: 'image/png' })
  }
}

export async function imageFileFromURL(url: string, fileName: string): Promise<File> {
  if (/^data:/i.test(url)) return imageFileFromDataURL(url, fileName)
  const response = await fetch(url)
  if (!response.ok) throw new Error('Image could not be read')
  const blob = await response.blob()
  return new File([blob], fileName, { type: blob.type || 'image/png' })
}

function imageFileFromDataURL(url: string, fileName: string): File {
  const separator = url.indexOf(',')
  if (separator < 5) throw new Error('Image could not be read')

  const metadata = url.slice(5, separator).split(';')
  const mimeType = metadata[0] || 'image/png'
  const payload = url.slice(separator + 1)
  try {
    if (!metadata.some((value) => value.toLowerCase() === 'base64')) {
      return new File([decodeURIComponent(payload)], fileName, { type: mimeType })
    }
    const binary = atob(payload.replace(/\s/g, ''))
    const bytes = new Uint8Array(binary.length)
    for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index)
    return new File([bytes], fileName, { type: mimeType })
  } catch {
    throw new Error('Image could not be read')
  }
}

function canvasBlob(canvas: HTMLCanvasElement): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => {
      if (blob) resolve(blob)
      else reject(new Error('Image could not be encoded'))
    }, 'image/png')
  })
}

function loadImage(url: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const image = new Image()
    image.onload = () => resolve(image)
    image.onerror = () => reject(new Error('Image could not be loaded'))
    image.src = url
  })
}
