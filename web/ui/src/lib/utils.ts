import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

export function cn(...inputs: ClassValue[]): string { return twMerge(clsx(inputs)) }

export function shortName(path: string): string {
  return path.split('/').filter(Boolean).pop() ?? path
}