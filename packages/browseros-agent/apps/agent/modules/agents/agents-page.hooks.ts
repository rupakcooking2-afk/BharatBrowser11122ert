import { type Dispatch, type SetStateAction, useEffect } from 'react'

export function useDefaultAgentName(
  createOpen: boolean,
  setNewName: Dispatch<SetStateAction<string>>,
): void {
  useEffect(() => {
    if (!createOpen) return
    setNewName((current) => current || 'agent')
  }, [createOpen, setNewName])
}
